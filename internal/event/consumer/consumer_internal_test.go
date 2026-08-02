package consumer

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/event/model"
)

// TestStoredEventBucketTimeDelegatesToModelEffectiveTime confirms the wiring every rollup writer in
// this package relies on: storedEvent.bucketTime() is model.EffectiveTime of the event's own two
// timestamps, not e.DeviceTime read directly. model's own tests cover EffectiveTime's clamp
// thresholds in full; this only proves bucketTime actually calls it rather than reimplementing or
// bypassing it.
func TestStoredEventBucketTimeDelegatesToModelEffectiveTime(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		deviceTime time.Time
		want       time.Time
	}{
		"ordinary device time before server time is used as-is": {
			deviceTime: serverTime.Add(-time.Hour),
			want:       serverTime.Add(-time.Hour),
		},
		"device time far into the future is clamped to server time": {
			deviceTime: serverTime.Add(72 * time.Hour),
			want:       serverTime,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e := storedEvent{DeviceTime: tt.deviceTime, ServerTime: serverTime}
			if got := e.bucketTime(); !got.Equal(tt.want) {
				t.Errorf("bucketTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEventNameUsesTheKindForEveryBuiltInKind is what keeps the rollup's event_name column stable
// for the platform's own kinds: an impression is counted under "impression" regardless of whatever
// Name the submitting device happened to set, so a device cannot fragment a campaign's rollup by
// filling in a free-text name alongside a built-in kind.
func TestEventNameUsesTheKindForEveryBuiltInKind(t *testing.T) {
	kinds := []model.Kind{
		model.KindImpression, model.KindButtonPress, model.KindDismissal, model.KindAutoClose,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			e := storedEvent{Kind: kind, Name: "device_supplied_name"}
			if got := e.eventName(); got != string(kind) {
				t.Errorf("eventName() = %q, want %q (the kind, not the submitted name)", got, kind)
			}
		})
	}
}

// TestEventNameUsesTheSubmittedNameForACustomEvent is the one kind whose name is the device's to
// choose: a custom event has no fixed name, and it is what CW-0004 Unit 5's event-aggregate
// attributes are targeted on, so the submitted name has to survive into the rollup verbatim.
func TestEventNameUsesTheSubmittedNameForACustomEvent(t *testing.T) {
	e := storedEvent{Kind: model.KindCustom, Name: "route_screen_view"}
	if got := e.eventName(); got != "route_screen_view" {
		t.Errorf("eventName() = %q, want %q", got, "route_screen_view")
	}
}
