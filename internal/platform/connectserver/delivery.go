package connectserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveryv1 "github.com/0x0c/citywalk/gen/citywalk/delivery/v1"
	"github.com/0x0c/citywalk/internal/delivery/confirm"
	"github.com/0x0c/citywalk/internal/delivery/deliver"
	"github.com/0x0c/citywalk/internal/delivery/payload"
	"github.com/0x0c/citywalk/internal/event/ingest"
	eventmodel "github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/governance/budget"
)

// defaultSyncConfig holds the phase-one synchronization parameters: no per-project configuration
// exists yet (the administrative interface, CW-0001 Unit 1, isn't built), so these are fixed
// constants rather than settings. sizeCeilingBytes follows CW-0010 Unit 1's own assumption of a 40
// kilobyte compressed full response.
var defaultSyncConfig = deliver.Config{
	SizeCeilingBytes:    40 * 1024,
	SyncInterval:        15 * time.Minute,
	SyncJitterFraction:  0.2,
	TagCacheTTL:         30 * time.Second,
	ProjectBudgetCap:    defaultProjectBudgetCap,
	ProjectBudgetWindow: defaultProjectBudgetWindow,
}

// DeliveryServer implements deliveryv1connect.DeliveryServiceHandler: Sync (CW-0006) and Confirm
// (CW-0002 Unit 4).
type DeliveryServer struct {
	Pool  *pgxpool.Pool
	Redis *redis.Client
}

func (s DeliveryServer) Sync(
	ctx context.Context,
	req *connect.Request[deliveryv1.SyncRequest],
) (*connect.Response[deliveryv1.SyncResponse], error) {
	if s.Redis == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("sync requires redis, which this process was started without"))
	}
	result, err := deliver.Sync(ctx, s.Pool, s.Redis, req.Msg.GetChannelId(), req.Msg.GetLanguage(), req.Msg.GetEtag(), time.Now(), defaultSyncConfig)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &deliveryv1.SyncResponse{
		Unchanged:  result.Unchanged,
		Etag:       result.ETag,
		NextSyncAt: timestamppb.New(result.NextSyncAt),
	}
	if result.Payload != nil {
		if remaining := result.Payload.ProjectBudgetRemaining; remaining != nil {
			r := int32(*remaining)
			resp.ProjectBudgetRemaining = &r
		}
		entries, err := wireEntries(result.Payload.Entries)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.Entries = entries
	}
	return connect.NewResponse(resp), nil
}

func wireEntries(entries []payload.Entry) ([]*deliveryv1.Entry, error) {
	wire := make([]*deliveryv1.Entry, len(entries))
	for i, e := range entries {
		triggersJSON, err := json.Marshal(e.Triggers)
		if err != nil {
			return nil, err
		}
		displayConditionsJSON, err := json.Marshal(e.DisplayConditions)
		if err != nil {
			return nil, err
		}
		controlPolicyJSON, err := json.Marshal(e.ControlPolicy)
		if err != nil {
			return nil, err
		}
		wire[i] = &deliveryv1.Entry{
			MessageId:             e.MessageID,
			Version:               int32(e.Version),
			VariantId:             e.VariantID,
			SchemaVersionMajor:    int32(e.SchemaVersion.Major),
			SchemaVersionMinor:    int32(e.SchemaVersion.Minor),
			Priority:              int32(e.Priority),
			Content:               e.Content,
			TriggersJson:          triggersJSON,
			DisplayConditionsJson: displayConditionsJSON,
			ControlPolicyJson:     controlPolicyJSON,
			ExpiresAt:             timestamppb.New(e.ExpiresAt),
		}
	}
	return wire, nil
}

// defaultProjectBudgetCap and defaultProjectBudgetWindow are CW-0007 Unit 4/5's phase-one project-wide
// impression budget: no per-project configuration exists yet (the administrative interface, CW-0001
// Unit 1, isn't built), so these are fixed constants, matching defaultSyncConfig's own reasoning.
// "Two per day" is the design doc's own example figure for the shape a campaign author writes a cap
// in.
const (
	defaultProjectBudgetCap    = 2
	defaultProjectBudgetWindow = 24 * time.Hour
)

func (s DeliveryServer) Confirm(
	ctx context.Context,
	req *connect.Request[deliveryv1.ConfirmRequest],
) (*connect.Response[deliveryv1.ConfirmResponse], error) {
	messageID := req.Msg.GetMessageId()
	now := time.Now()

	approved, err := confirm.Confirm(ctx, s.Pool, messageID, now)
	if err != nil {
		// Propagated rather than swallowed into a false-but-200-OK response: FR-API-08 and CW-0002
		// Unit 4 both specify that an error suppresses the display exactly like an explicit no, so
		// there is no correctness reason to hide this from the caller — and hiding it would also
		// hide it from the OpenTelemetry interceptor wrapping this handler.
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// CW-0007 Unit 6 layers the project-wide budget's atomic decrement onto CW-0002 Unit 4's own
	// eligibility check, rather than folding it into confirm.Confirm itself: the two are separately
	// owned concerns (message eligibility vs. platform-wide comfort threshold), and a request already
	// denied by Confirm has nothing left to check the budget against. Redis is optional in phase one
	// (CW-0010 Unit 11); without it, the budget simply isn't enforced, matching Sync's own
	// per-dependency degradation rather than failing every Confirm call for want of a dependency
	// Confirm's original eligibility check doesn't need.
	if approved && s.Redis != nil {
		approved, err = s.checkProjectBudget(ctx, messageID, req.Msg.GetChannelId(), now)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return connect.NewResponse(&deliveryv1.ConfirmResponse{Approved: approved}), nil
}

// checkProjectBudget is CW-0007 Unit 6's atomic half: exempt campaigns bypass the budget entirely
// (Unit 5), and every other campaign's confirmation atomically checks and spends one unit of
// channelID's rolling budget, emitting the one suppression reason a server-side decision can ever
// produce — project_budget — when it's exhausted.
func (s DeliveryServer) checkProjectBudget(ctx context.Context, messageID, channelID string, now time.Time) (bool, error) {
	exempt, err := isExemptFromProjectCap(ctx, s.Pool, messageID)
	if err != nil {
		return false, err
	}
	if exempt {
		return true, nil
	}

	counter := budget.Counter{Redis: s.Redis, Window: defaultProjectBudgetWindow}
	allowed, err := counter.CheckAndIncrement(ctx, channelID, defaultProjectBudgetCap, now)
	if err != nil {
		return false, fmt.Errorf("checkProjectBudget: %w", err)
	}
	if !allowed {
		suppression := eventmodel.Event{
			ID:                uuid.NewString(),
			ChannelID:         channelID,
			Kind:              eventmodel.KindSuppression,
			DeviceTime:        now,
			MessageID:         messageID,
			SuppressionReason: eventmodel.ReasonProjectBudget,
		}
		if err := ingest.Record(ctx, s.Pool, suppression, now); err != nil {
			return false, fmt.Errorf("checkProjectBudget: record suppression: %w", err)
		}
	}
	return allowed, nil
}

func isExemptFromProjectCap(ctx context.Context, pool *pgxpool.Pool, messageID string) (bool, error) {
	var exempt bool
	err := pool.QueryRow(ctx,
		`SELECT exempt_from_project_cap FROM control_policies WHERE message_id = $1`, messageID,
	).Scan(&exempt)
	if err != nil {
		return false, fmt.Errorf("isExemptFromProjectCap: message %s: %w", messageID, err)
	}
	return exempt, nil
}
