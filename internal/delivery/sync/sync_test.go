package sync_test

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/delivery/sync"
)

func TestNextSyncAtStaysWithinTheJitterWindow(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	baseInterval := 15 * time.Minute
	jitterFraction := 0.2 // up to 3 minutes of spread

	earliest := now.Add(baseInterval)
	latest := now.Add(baseInterval).Add(time.Duration(float64(baseInterval) * jitterFraction))

	for i := 0; i < 200; i++ {
		got := sync.NextSyncAt(now, baseInterval, jitterFraction)
		if got.Before(earliest) || !got.Before(latest) {
			t.Fatalf("NextSyncAt = %v, want within [%v, %v)", got, earliest, latest)
		}
	}
}

func TestNextSyncAtSpreadsAcrossTheWindowRatherThanAlwaysReturningTheFloor(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	baseInterval := 15 * time.Minute

	seen := make(map[time.Time]bool)
	for i := 0; i < 50; i++ {
		seen[sync.NextSyncAt(now, baseInterval, 0.2)] = true
	}
	// With a real random offset, 50 draws over a multi-minute window essentially never collapse to
	// one value; this is the thundering-herd property Unit 3 exists to avoid.
	if len(seen) < 2 {
		t.Errorf("got %d distinct sync times across 50 draws, want more than 1 (jitter should spread them)", len(seen))
	}
}

func TestNextSyncAtWithZeroJitterIsExactlyTheInterval(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	baseInterval := 15 * time.Minute

	got := sync.NextSyncAt(now, baseInterval, 0)
	want := now.Add(baseInterval)
	if !got.Equal(want) {
		t.Errorf("NextSyncAt with zero jitter = %v, want exactly %v", got, want)
	}
}
