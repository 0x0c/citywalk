package consumer

import (
	"testing"
	"time"
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
