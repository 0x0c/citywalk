package assign_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/experiment/assign"
)

// TestBucketParityFixtures is the server-side half of CW-0008 Unit 6's parity test: a fixed set of
// (salt, identity) pairs with their expected buckets, locked in once and never recomputed. A device
// implementation of the same bucketing function (client SDK code, out of scope for this repository)
// is correct exactly when it reproduces this same table — that is what "parity" means, and this
// fixture is the contract a device implementation would be checked against. It also protects this
// package itself: if Bucket's hash, separator, or modulo ever changes by accident, this test fails
// immediately rather than silently reassigning every identity in production.
func TestBucketParityFixtures(t *testing.T) {
	cases := []struct {
		salt, identity string
		wantBucket     int
	}{
		{"exp-checkout-button-color", "user-1", 457},
		{"exp-checkout-button-color", "user-2", 5824},
		{"exp-checkout-button-color", "channel-abc-123", 7879},
		{"exp-onboarding-flow", "user-1", 7106},
		{"", "user-1", 2678},
		{"exp-checkout-button-color", "", 6792},
		{"project-holdout-2026", "user-42", 5497},
	}

	for _, tc := range cases {
		t.Run(tc.salt+"/"+tc.identity, func(t *testing.T) {
			got := assign.Bucket(tc.salt, tc.identity)
			if got != tc.wantBucket {
				t.Errorf("Bucket(%q, %q) = %d, want %d", tc.salt, tc.identity, got, tc.wantBucket)
			}
		})
	}
}
