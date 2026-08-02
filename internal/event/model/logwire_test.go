package model_test

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/event/model"
)

// TestEncodeLogRoundTrips confirms every field EncodeLog writes comes back unchanged from
// DecodeLogEvent, for both a plain custom event and a fully populated impression-family one — the
// message encoding/decoding CW-0009 Unit 3's log-backed publisher and consumer rely on, testable
// without a live broker.
func TestEncodeLogRoundTrips(t *testing.T) {
	deviceTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	serverTime := deviceTime.Add(2 * time.Second)

	tests := map[string]model.Event{
		"custom event": {
			ID: "01912d2c-0000-7000-8000-000000000001", ChannelID: "channel-a",
			Kind: model.KindCustom, Name: "screen_view",
			DeviceTime: deviceTime, ServerTime: serverTime,
			Properties: map[string]any{"screen": "home", "count": float64(3)},
		},
		"impression event": {
			ID: "01912d2c-0000-7000-8000-000000000002", ChannelID: "channel-b",
			Kind: model.KindImpression, MessageID: "message-1", VariantID: "variant-1",
			DeviceTime: deviceTime, ServerTime: serverTime,
		},
		"suppression event": {
			ID: "01912d2c-0000-7000-8000-000000000003", ChannelID: "channel-c",
			Kind: model.KindSuppression, MessageID: "message-2", SuppressionReason: model.ReasonCooldown,
			DeviceTime: deviceTime, ServerTime: serverTime,
		},
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := want.EncodeLog()
			if err != nil {
				t.Fatalf("EncodeLog: %v", err)
			}
			got, err := model.DecodeLogEvent(data)
			if err != nil {
				t.Fatalf("DecodeLogEvent: %v", err)
			}
			if got.ID != want.ID || got.ChannelID != want.ChannelID || got.Kind != want.Kind ||
				got.Name != want.Name || got.MessageID != want.MessageID || got.VariantID != want.VariantID ||
				got.SuppressionReason != want.SuppressionReason {
				t.Errorf("round trip = %+v, want %+v", got, want)
			}
			if !got.DeviceTime.Equal(want.DeviceTime) {
				t.Errorf("DeviceTime = %v, want %v", got.DeviceTime, want.DeviceTime)
			}
			if !got.ServerTime.Equal(want.ServerTime) {
				t.Errorf("ServerTime = %v, want %v", got.ServerTime, want.ServerTime)
			}
			if len(want.Properties) != 0 {
				for k, v := range want.Properties {
					if got.Properties[k] != v {
						t.Errorf("Properties[%q] = %v, want %v", k, got.Properties[k], v)
					}
				}
			}
		})
	}
}

// TestDecodeLogEventRejectsMalformedJSON confirms a corrupt or foreign payload on the topic fails
// decode with an error rather than silently producing a zero-value event a consumer would apply as if
// it were real.
func TestDecodeLogEventRejectsMalformedJSON(t *testing.T) {
	if _, err := model.DecodeLogEvent([]byte("not json")); err == nil {
		t.Fatal("DecodeLogEvent: got nil error for malformed input, want one")
	}
}
