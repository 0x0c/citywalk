package connectserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	adminv1 "github.com/0x0c/citywalk/gen/citywalk/admin/v1"
	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/definition/validate"
	"github.com/0x0c/citywalk/internal/delivery/changelog"
	"github.com/0x0c/citywalk/internal/platform/adminauth"
)

// AdminServer implements adminv1connect.AdminServiceHandler: CW-0001 Unit 1's administrative
// surface, authenticated and role-checked by adminAuthInterceptor before any of these methods runs.
type AdminServer struct {
	Pool *pgxpool.Pool
}

func (s AdminServer) CreateMessage(
	ctx context.Context,
	req *connect.Request[adminv1.CreateMessageRequest],
) (*connect.Response[adminv1.CreateMessageResponse], error) {
	msg, err := fromMessageDefinition(req.Msg.GetMessage())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// A created message always starts in draft (FR-MSG-01) regardless of what the request set;
	// UpdateMessageState is the only path that moves it from there. Draft is never eligible for any
	// channel (internal/delivery/payload's eligibleMessageIDs filters on state = 'active'), so
	// CW-0006 Unit 3's change log needs no write here — only UpdateMessageState can ever be the
	// event that makes a message eligible for the first time.
	msg.State = model.MessageStateDraft

	if err := validate.Validate(ctx, s.Pool, msg, time.Now()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := store.InsertMessage(ctx, s.Pool, &msg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&adminv1.CreateMessageResponse{MessageId: msg.ID}), nil
}

func (s AdminServer) UpdateMessageState(
	ctx context.Context,
	req *connect.Request[adminv1.UpdateMessageStateRequest],
) (*connect.Response[adminv1.UpdateMessageStateResponse], error) {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no authenticated principal in context — adminAuthInterceptor is not wired in front of this handler"))
	}

	newState := model.MessageState(req.Msg.GetNewState())
	now := time.Now()
	if err := store.UpdateState(ctx, s.Pool, req.Msg.GetMessageId(), newState, principal.Subject, now); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	// CW-0006 Unit 3's change log: the kill switch is the only mutation surface that can move a
	// message's eligibility today (see CreateMessage's own comment on why creation itself needs no
	// entry), so this is the one hook point that needs it, matching CW-0006 Unit 1's entity tag —
	// content-derived, recomputed at request time rather than invalidated on write — which is why no
	// analogous hook exists anywhere in this codebase to mirror before this pass. Kind is derived
	// from the destination state alone (see changelog.Kind's own doc comment on why that's enough):
	// this write does not need to know the message's prior state to be correct.
	kind := changelog.KindTombstone
	if newState == model.MessageStateActive {
		kind = changelog.KindUpsert
	}
	// Not folded into store.UpdateState's own transaction — that would need a signature change
	// touching every one of that function's existing callers for a table only this handler writes
	// to. A failure here after the state transition already committed is reported rather than
	// swallowed (this codebase's standing rule against hiding a failure the requirements need
	// observable), at the cost of the caller seeing an error for a state change that did apply; a
	// retry finds CanTransition already satisfied (the target state accepts itself as a no-op) and
	// succeeds, writing the change log entry it was missing.
	if err := changelog.Record(ctx, s.Pool, req.Msg.GetMessageId(), kind, now); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&adminv1.UpdateMessageStateResponse{State: string(newState)}), nil
}

func (s AdminServer) GetMessage(
	ctx context.Context,
	req *connect.Request[adminv1.GetMessageRequest],
) (*connect.Response[adminv1.GetMessageResponse], error) {
	msg, err := store.GetMessage(ctx, s.Pool, req.Msg.GetMessageId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	wire, err := toMessageDefinition(msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&adminv1.GetMessageResponse{Message: wire}), nil
}

func (s AdminServer) ListAuditLog(
	ctx context.Context,
	req *connect.Request[adminv1.ListAuditLogRequest],
) (*connect.Response[adminv1.ListAuditLogResponse], error) {
	entries, err := store.ListAuditLog(ctx, s.Pool, req.Msg.GetMessageId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	wire := make([]*adminv1.AuditLogEntry, len(entries))
	for i, e := range entries {
		wire[i] = &adminv1.AuditLogEntry{
			FromState:  string(e.FromState),
			ToState:    string(e.ToState),
			OccurredAt: timestamppb.New(e.OccurredAt),
			Actor:      e.Actor,
		}
	}
	return connect.NewResponse(&adminv1.ListAuditLogResponse{Entries: wire}), nil
}

// variantWire is the JSON shape a MessageDefinition's variants_json field carries: Variant's
// existing MarshalContentColumn/UnmarshalContentColumn helpers already handle the tagged-union
// Content field (CW-0003 Unit 2) correctly, so this wraps those rather than reimplementing
// tagged-union encoding for the admin wire format.
type variantWire struct {
	ID            string          `json:"id"`
	Weight        int             `json:"weight"`
	Language      string          `json:"language"`
	ContentColumn json.RawMessage `json:"content_column"`
}

func marshalVariants(variants []model.Variant) ([]byte, error) {
	wire := make([]variantWire, len(variants))
	for i, v := range variants {
		contentColumn, err := v.MarshalContentColumn()
		if err != nil {
			return nil, fmt.Errorf("marshal variant %s: %w", v.ID, err)
		}
		wire[i] = variantWire{ID: v.ID, Weight: v.Weight, Language: v.Language, ContentColumn: contentColumn}
	}
	return json.Marshal(wire)
}

func unmarshalVariants(data []byte) ([]model.Variant, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var wire []variantWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("decode variants_json: %w", err)
	}
	variants := make([]model.Variant, len(wire))
	for i, w := range wire {
		v := model.Variant{ID: w.ID, Weight: w.Weight, Language: w.Language}
		if err := v.UnmarshalContentColumn(w.ContentColumn); err != nil {
			return nil, fmt.Errorf("decode variant %d content: %w", i, err)
		}
		variants[i] = v
	}
	return variants, nil
}

// toMessageDefinition and fromMessageDefinition convert between model.Message and its wire
// projection, encoding the nested entities as JSON — the same choice
// internal/platform/connectserver/delivery.go's wireEntries already made for the device-facing wire
// format, for the same reason: no administrative client exists in this repository yet to justify a
// second, protobuf-native schema for entities CW-0003 already models fully in Go.
func toMessageDefinition(msg model.Message) (*adminv1.MessageDefinition, error) {
	controlPolicyJSON, err := json.Marshal(msg.ControlPolicy)
	if err != nil {
		return nil, fmt.Errorf("marshal control_policy: %w", err)
	}
	triggersJSON, err := json.Marshal(msg.Triggers)
	if err != nil {
		return nil, fmt.Errorf("marshal triggers: %w", err)
	}
	displayConditionsJSON, err := json.Marshal(msg.DisplayConditions)
	if err != nil {
		return nil, fmt.Errorf("marshal display_conditions: %w", err)
	}
	variantsJSON, err := marshalVariants(msg.Variants)
	if err != nil {
		return nil, err
	}

	return &adminv1.MessageDefinition{
		Id:                    msg.ID,
		Name:                  msg.Name,
		State:                 string(msg.State),
		Priority:              int32(msg.Priority),
		WindowStart:           timestamppb.New(msg.Window.Start),
		WindowEnd:             timestamppb.New(msg.Window.End),
		AudienceRef:           msg.AudienceRef,
		HoldoutFraction:       msg.HoldoutFraction,
		ConversionEvent:       msg.ConversionEvent,
		Version:               int32(msg.Version),
		ExperimentSalt:        msg.ExperimentSalt,
		ControlPolicyJson:     controlPolicyJSON,
		TriggersJson:          triggersJSON,
		DisplayConditionsJson: displayConditionsJSON,
		VariantsJson:          variantsJSON,
	}, nil
}

func fromMessageDefinition(w *adminv1.MessageDefinition) (model.Message, error) {
	var controlPolicy model.ControlPolicy
	if raw := w.GetControlPolicyJson(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &controlPolicy); err != nil {
			return model.Message{}, fmt.Errorf("decode control_policy_json: %w", err)
		}
	}
	var triggers []model.Trigger
	if raw := w.GetTriggersJson(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &triggers); err != nil {
			return model.Message{}, fmt.Errorf("decode triggers_json: %w", err)
		}
	}
	var displayConditions []model.DisplayCondition
	if raw := w.GetDisplayConditionsJson(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &displayConditions); err != nil {
			return model.Message{}, fmt.Errorf("decode display_conditions_json: %w", err)
		}
	}
	variants, err := unmarshalVariants(w.GetVariantsJson())
	if err != nil {
		return model.Message{}, err
	}

	return model.Message{
		Name:              w.GetName(),
		State:             model.MessageState(w.GetState()),
		Priority:          int(w.GetPriority()),
		Window:            model.Window{Start: w.GetWindowStart().AsTime(), End: w.GetWindowEnd().AsTime()},
		AudienceRef:       w.GetAudienceRef(),
		HoldoutFraction:   w.GetHoldoutFraction(),
		ConversionEvent:   w.GetConversionEvent(),
		ControlPolicy:     controlPolicy,
		Triggers:          triggers,
		DisplayConditions: displayConditions,
		Variants:          variants,
	}, nil
}

// adminRoleByProcedure is adminAuthInterceptor's role requirement per AdminService procedure —
// mutations need at least editor, reads need at least viewer, matching admin.proto's own per-RPC doc
// comments.
var adminRoleByProcedure = map[string]adminauth.Role{
	"/citywalk.admin.v1.AdminService/CreateMessage":      adminauth.RoleEditor,
	"/citywalk.admin.v1.AdminService/UpdateMessageState": adminauth.RoleEditor,
	"/citywalk.admin.v1.AdminService/GetMessage":         adminauth.RoleViewer,
	"/citywalk.admin.v1.AdminService/ListAuditLog":       adminauth.RoleViewer,
}
