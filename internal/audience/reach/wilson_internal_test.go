package reach

import (
	"math"
	"testing"
)

// TestWilsonIntervalMatchesKnownValues checks wilsonInterval against textbook Wilson score interval
// values, computed independently — the deterministic check that the formula itself is right.
// reach_integration_test.go then only needs a wide, always-true sanity bound around real sampling,
// rather than re-asserting statistical coverage (which is probabilistic by nature) on every run.
func TestWilsonIntervalMatchesKnownValues(t *testing.T) {
	const tolerance = 0.001
	cases := []struct {
		name              string
		matches, n        int
		z                 float64
		wantLow, wantHigh float64
	}{
		// n=100, matches=50, z=1.96 (95%): a standard textbook example, interval ≈ [0.4038, 0.5962].
		{"100 trials half matching", 50, 100, 1.96, 0.40383, 0.59617},
		// n=10, matches=10 (all match): Wilson correctly stays below 1, unlike the normal
		// approximation, which would degenerate to a zero-width interval at the boundary.
		{"all match, small n", 10, 10, 1.96, 0.72246, 1.0},
		// n=10, matches=0 (none match): symmetric case, interval stays above 0.
		{"none match, small n", 0, 10, 1.96, 0.0, 0.27754},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			low, center, high := wilsonInterval(tc.matches, tc.n, tc.z)
			if math.Abs(low-tc.wantLow) > tolerance {
				t.Errorf("low = %v, want %v (+/- %v)", low, tc.wantLow, tolerance)
			}
			if math.Abs(high-tc.wantHigh) > tolerance {
				t.Errorf("high = %v, want %v (+/- %v)", high, tc.wantHigh, tolerance)
			}
			if center < low || center > high {
				t.Errorf("center %v is outside [%v, %v]", center, low, high)
			}
		})
	}
}

func TestWilsonIntervalStaysWithinZeroAndOne(t *testing.T) {
	for _, tc := range []struct{ matches, n int }{
		{0, 1}, {1, 1}, {0, 1000}, {1000, 1000}, {500, 1000},
	} {
		low, _, high := wilsonInterval(tc.matches, tc.n, 1.96)
		if low < 0 || high > 1 {
			t.Errorf("wilsonInterval(%d, %d) = [%v, %v], want within [0, 1]", tc.matches, tc.n, low, high)
		}
	}
}

func TestWilsonIntervalOnZeroTrialsIsZero(t *testing.T) {
	low, center, high := wilsonInterval(0, 0, 1.96)
	if low != 0 || center != 0 || high != 0 {
		t.Errorf("wilsonInterval(0, 0, _) = (%v, %v, %v), want (0, 0, 0)", low, center, high)
	}
}
