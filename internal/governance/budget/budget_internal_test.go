package budget

import (
	"strings"
	"testing"
	"time"
)

// TestKeyNamespacesEveryIdentityUnderTheBudgetPrefix is the collision guard CW-0010 Unit 4's shared
// Redis instance needs: CW-0005's reverse index and CW-0006's entity-tag cache live in the same
// keyspace, so an unprefixed identity would let one subsystem overwrite another's value.
func TestKeyNamespacesEveryIdentityUnderTheBudgetPrefix(t *testing.T) {
	c := Counter{Window: time.Hour}

	got := c.key("channel-1")
	if !strings.HasPrefix(got, "citywalk:budget:") {
		t.Errorf("key(%q) = %q, want it prefixed with %q", "channel-1", got, "citywalk:budget:")
	}
	if got == c.key("channel-2") {
		t.Errorf("key(%q) == key(%q) = %q, want distinct keys", "channel-1", "channel-2", got)
	}
}

// TestWindowParamsAdvancesTheIndexExactlyOncePerWindow is what makes the counter roll over rather
// than accumulate forever: two instants inside one window must share a bucket, and the instant just
// past the boundary must not.
func TestWindowParamsAdvancesTheIndexExactlyOncePerWindow(t *testing.T) {
	const window = time.Hour
	start := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	startIdx, _ := windowParams(start, window)
	lateIdx, _ := windowParams(start.Add(59*time.Minute+59*time.Second), window)
	if startIdx != lateIdx {
		t.Errorf("window index at the start (%d) and end (%d) of one window differ, want the same bucket", startIdx, lateIdx)
	}

	nextIdx, _ := windowParams(start.Add(window), window)
	if nextIdx != startIdx+1 {
		t.Errorf("window index one window later = %d, want %d", nextIdx, startIdx+1)
	}
}

// TestWindowParamsWeightsThePreviousBucketByTheTimeLeftInThisOne is CW-0007 Unit 4's sliding-window
// formula: at a window boundary the previous bucket counts in full, and its contribution decays
// linearly to nothing as the current window elapses. Without that decay the counter would either
// forget a window's spend the instant it rolled over, or double-count across the boundary.
func TestWindowParamsWeightsThePreviousBucketByTheTimeLeftInThisOne(t *testing.T) {
	const window = time.Hour
	boundary := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		at   time.Time
		want float64
	}{
		"at the boundary":   {boundary, 1},
		"a quarter elapsed": {boundary.Add(15 * time.Minute), 0.75},
		"halfway through":   {boundary.Add(30 * time.Minute), 0.5},
		"nearly the next":   {boundary.Add(45 * time.Minute), 0.25},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, got := windowParams(tc.at, window)
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("previous-bucket weight at %v = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

// TestWindowParamsWeightStaysWithinZeroAndOne holds the invariant the Lua script downstream relies
// on: the weight is a fraction, so a value outside [0, 1] would either erase or inflate the previous
// window's spend.
func TestWindowParamsWeightStaysWithinZeroAndOne(t *testing.T) {
	const window = 5 * time.Minute
	start := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	for offset := time.Duration(0); offset < 2*window; offset += time.Second {
		_, weight := windowParams(start.Add(offset), window)
		if weight < 0 || weight > 1 {
			t.Fatalf("weight at offset %v = %v, want it within [0, 1]", offset, weight)
		}
	}
}
