package consumer

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/event/model"
)

// TestEffectiveTimeReturnsDeviceTimeWhenNotAfterServerTime covers both the ordinary case (device time
// strictly before server time — the vast majority of events, submitted online or with a small,
// unremarkable delay) and the equal-timestamps edge case. Either endpoint is a defensible answer when
// the two are equal; effectiveTime's "not after" comparison resolves the tie in device time's favor,
// so this test asserts that specific choice rather than leaving it unstated.
func TestEffectiveTimeReturnsDeviceTimeWhenNotAfterServerTime(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	tests := map[string]time.Time{
		"before server time": serverTime.Add(-time.Hour),
		"equal server time":  serverTime,
	}

	for name, deviceTime := range tests {
		t.Run(name, func(t *testing.T) {
			got := effectiveTime(deviceTime, serverTime)
			if !got.Equal(deviceTime) {
				t.Errorf("effectiveTime(%v, %v) = %v, want %v (device time)", deviceTime, serverTime, got, deviceTime)
			}
		})
	}
}

// TestEffectiveTimeClampsDeviceTimeAfterServerTimeToServerTime is Unit 1's clamp rule itself: a
// device clock set into the future — by accident or by tampering — must never push a rollup bucket
// past the day the server has actually reached.
func TestEffectiveTimeClampsDeviceTimeAfterServerTimeToServerTime(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	deviceTime := serverTime.Add(72 * time.Hour)

	got := effectiveTime(deviceTime, serverTime)
	if !got.Equal(serverTime) {
		t.Errorf("effectiveTime(%v, %v) = %v, want %v (clamped to server time)", deviceTime, serverTime, got, serverTime)
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

// TestBucketTimeCountsAnEventOnItsDeviceDayUnlessThatDayIsStillAhead ties the clamp to the thing it
// actually protects — which day's bucket an event lands in. A device clock a week fast would
// otherwise create rollup rows for days the server has not reached, which no report covering "the
// last seven days" would ever surface again.
func TestBucketTimeCountsAnEventOnItsDeviceDayUnlessThatDayIsStillAhead(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	offline := storedEvent{DeviceTime: serverTime.Add(-48 * time.Hour), ServerTime: serverTime}
	if got := offline.bucketTime(); !got.Equal(offline.DeviceTime) {
		t.Errorf("bucketTime() for a queued offline event = %v, want its device time %v", got, offline.DeviceTime)
	}

	fastClock := storedEvent{DeviceTime: serverTime.Add(168 * time.Hour), ServerTime: serverTime}
	if got := fastClock.bucketTime(); !got.Equal(serverTime) {
		t.Errorf("bucketTime() for a device clock a week fast = %v, want it clamped to %v", got, serverTime)
	}
}
