package connectserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/go-redis/v9"

	eventv1 "github.com/0x0c/citywalk/gen/citywalk/event/v1"
	"github.com/0x0c/citywalk/internal/event/ingest"
	eventmodel "github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/event/ratelimit"
)

// defaultRateLimit is the phase-one per-channel ceiling (CW-0009 Unit 2): no per-project
// configuration exists yet (the administrative interface, CW-0001 Unit 1, isn't built), so this is
// a fixed constant. 1,000 events per minute is generous for legitimate use — a device fires events
// on user action and screen views, not continuously — while still bounding what one device stuck in
// a render-loop bug can cost the platform.
var defaultRateLimit = ratelimit.Limiter{Limit: 1000, Window: time.Minute}

// EventServer implements eventv1connect.EventServiceHandler: Submit (CW-0009 Unit 2).
type EventServer struct {
	// Publisher is where Submit's accepted events land (CW-0010 Unit 11's staged adoption). NewMux
	// selects it from internal/platform/config's event publisher mode.
	Publisher ingest.Publisher
	Redis     *redis.Client
}

func (s EventServer) Submit(
	ctx context.Context,
	req *connect.Request[eventv1.SubmitRequest],
) (*connect.Response[eventv1.SubmitResponse], error) {
	if s.Redis == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("submit requires redis, which this process was started without"))
	}
	channelID, err := requireChannelID(ctx)
	if err != nil {
		return nil, err
	}

	events := make([]eventmodel.Event, len(req.Msg.GetEvents()))
	for i, wire := range req.Msg.GetEvents() {
		e, err := fromWire(wire)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		// The authenticated channel identity wins over whatever the wire event claims — CW-0010
		// Unit 9's "naming another channel's identifier grants nothing" applies per event, not just
		// to the batch-level field, since ingest.Accept itself requires every event in a batch to
		// share one channel_id and would otherwise reject a batch whose per-event value disagreed
		// with the (now ignored) request-level one.
		e.ChannelID = channelID
		events[i] = e
	}

	limiter := defaultRateLimit
	limiter.Redis = s.Redis
	result, err := ingest.Accept(ctx, s.Publisher, limiter, channelID, events, time.Now())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&eventv1.SubmitResponse{
		Accepted:            int32(result.Accepted),
		RejectedInvalid:     int32(result.RejectedInvalid),
		RejectedRateLimited: int32(result.RejectedRateLimited),
	}), nil
}

func fromWire(wire *eventv1.Event) (eventmodel.Event, error) {
	var properties map[string]any
	if raw := wire.GetPropertiesJson(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &properties); err != nil {
			return eventmodel.Event{}, fmt.Errorf("event %s: decode properties_json: %w", wire.GetId(), err)
		}
	}
	return eventmodel.Event{
		ID:                wire.GetId(),
		ChannelID:         wire.GetChannelId(),
		Kind:              eventmodel.Kind(wire.GetKind()),
		Name:              wire.GetName(),
		DeviceTime:        wire.GetDeviceTime().AsTime(),
		Properties:        properties,
		MessageID:         wire.GetMessageId(),
		VariantID:         wire.GetVariantId(),
		SuppressionReason: eventmodel.SuppressionReason(wire.GetSuppressionReason()),
	}, nil
}
