package consumer

import (
	"testing"
	"time"
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
