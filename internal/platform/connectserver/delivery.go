package connectserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveryv1 "github.com/0x0c/citywalk/gen/citywalk/delivery/v1"
	"github.com/0x0c/citywalk/internal/delivery/confirm"
	"github.com/0x0c/citywalk/internal/delivery/deliver"
	"github.com/0x0c/citywalk/internal/delivery/payload"
)

// defaultSyncConfig holds the phase-one synchronization parameters: no per-project configuration
// exists yet (the administrative interface, CW-0001 Unit 1, isn't built), so these are fixed
// constants rather than settings. sizeCeilingBytes follows CW-0010 Unit 1's own assumption of a 40
// kilobyte compressed full response.
var defaultSyncConfig = deliver.Config{
	SizeCeilingBytes:   40 * 1024,
	SyncInterval:       15 * time.Minute,
	SyncJitterFraction: 0.2,
	TagCacheTTL:        30 * time.Second,
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

func (s DeliveryServer) Confirm(
	ctx context.Context,
	req *connect.Request[deliveryv1.ConfirmRequest],
) (*connect.Response[deliveryv1.ConfirmResponse], error) {
	approved, err := confirm.Confirm(ctx, s.Pool, req.Msg.GetMessageId(), time.Now())
	if err != nil {
		// Propagated rather than swallowed into a false-but-200-OK response: FR-API-08 and CW-0002
		// Unit 4 both specify that an error suppresses the display exactly like an explicit no, so
		// there is no correctness reason to hide this from the caller — and hiding it would also
		// hide it from the OpenTelemetry interceptor wrapping this handler.
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&deliveryv1.ConfirmResponse{Approved: approved}), nil
}
