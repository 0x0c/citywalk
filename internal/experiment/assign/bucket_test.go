package assign_test

import (
	"fmt"
	"testing"

	"github.com/0x0c/citywalk/internal/experiment/assign"
)

func TestBucketIsWithinRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		b := assign.Bucket("salt", fmt.Sprintf("identity-%d", i))
		if b < 0 || b >= assign.BucketCount {
			t.Fatalf("Bucket(%d) = %d, want within [0, %d)", i, b, assign.BucketCount)
		}
	}
}

func TestBucketIsDeterministic(t *testing.T) {
	a := assign.Bucket("salt", "identity")
	b := assign.Bucket("salt", "identity")
	if a != b {
		t.Errorf("Bucket returned %d then %d for the same inputs, want identical", a, b)
	}
}

func TestBucketDiffersByExperimentSalt(t *testing.T) {
	// CW-0008 Unit 2: two experiments must not correlate arm membership for the same identity. This
	// does not prove independence (that's TestBucketUniformity's job), only that a shared-salt bug
	// (identical bucket regardless of salt) is caught immediately. The two salts below are fixed,
	// pre-verified to bucket "user-1" differently (457 vs. 7106) — using a known-distinct pair
	// rather than asserting inequality on arbitrary salts, which would have a 1-in-10000 chance of
	// coinciding and would make this test flaky.
	a := assign.Bucket("exp-checkout-button-color", "user-1")
	b := assign.Bucket("exp-onboarding-flow", "user-1")
	if a == b {
		t.Errorf("Bucket returned %d for both salts with the same identity, want them to differ", a)
	}
}

func TestBucketSeparatesSaltFromIdentity(t *testing.T) {
	// Without a separator between salt and identity, ("a", "bc") and ("ab", "c") would hash the
	// exact same byte string ("abc") and collide with certainty, not merely by chance. Pinning both
	// sides to their known, pre-verified values (rather than only asserting a != b, which a
	// coincidental collision on an unrelated pair could fail 1 time in 10000) makes this check fully
	// deterministic.
	if got := assign.Bucket("a", "bc"); got != 4899 {
		t.Errorf(`Bucket("a", "bc") = %d, want 4899`, got)
	}
	if got := assign.Bucket("ab", "c"); got != 4135 {
		t.Errorf(`Bucket("ab", "c") = %d, want 4135`, got)
	}
}

// TestBucketUniformity is CW-0008 Unit 6's uniformity check: the bucketing function run over a large
// synthetic identity population must not deviate from a uniform distribution beyond a threshold.
// Every input is a deterministic string (identity-0, identity-1, ...), so this test's outcome is
// fully determined by the hash function itself — it involves no randomness and cannot flake between
// runs. The threshold is deliberately generous (the chi-square statistic would have to be double a
// true-uniform hash's expected value, on the order of 70 standard deviations away) so it fails only
// for a real bias, never for ordinary statistical noise.
func TestBucketUniformity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the 2,000,000-sample uniformity check in -short mode")
	}
	const n = 2_000_000
	const salt = "uniformity-check-salt"

	counts := make([]int, assign.BucketCount)
	for i := 0; i < n; i++ {
		counts[assign.Bucket(salt, fmt.Sprintf("identity-%d", i))]++
	}

	expected := float64(n) / float64(assign.BucketCount)
	chiSquare := 0.0
	for _, c := range counts {
		diff := float64(c) - expected
		chiSquare += diff * diff / expected
	}

	degreesOfFreedom := float64(assign.BucketCount - 1)
	threshold := 2 * degreesOfFreedom // deliberately loose; see doc comment above
	if chiSquare > threshold {
		t.Errorf("chi-square statistic = %v, want <= %v (expected count per bucket %v)", chiSquare, threshold, expected)
	}
}
