package assign_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/experiment/assign"
)

func TestAssignReturnsTheLookedUpVariant(t *testing.T) {
	table, err := assign.NewRangeTable([]string{"A", "B"}, map[string]int{"A": 5000, "B": 4500}, 500)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}

	result, err := assign.Assign(table, "exp-checkout-button-color", "user-1")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	// user-1 under this salt buckets to 457 (fixed by the hash function; see bucket_test.go's
	// parity fixtures), which falls in A's [0, 5000) range.
	if result.Bucket != 457 {
		t.Fatalf("Bucket = %d, want 457", result.Bucket)
	}
	if result.Variant != "A" || result.IsHoldout {
		t.Errorf("Assign = %+v, want variant A, not holdout", result)
	}
}

func TestAssignIsStableAcrossRepeatedCalls(t *testing.T) {
	table, err := assign.NewRangeTable([]string{"A", "B"}, map[string]int{"A": 5000, "B": 5000}, 0)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}

	first, err := assign.Assign(table, "salt", "identity")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	for i := 0; i < 10; i++ {
		again, err := assign.Assign(table, "salt", "identity")
		if err != nil {
			t.Fatalf("Assign: %v", err)
		}
		if again != first {
			t.Fatalf("Assign returned %+v on call %d, want it identical to the first call %+v", again, i, first)
		}
	}
}

func TestInProjectHoldout(t *testing.T) {
	// project-holdout-2026 / user-42 buckets to 5497 (see bucket_test.go's parity fixtures).
	if !assign.InProjectHoldout("project-holdout-2026", 5498, "user-42") {
		t.Error("InProjectHoldout = false with a threshold just above the identity's bucket, want true")
	}
	if assign.InProjectHoldout("project-holdout-2026", 5497, "user-42") {
		t.Error("InProjectHoldout = true with a threshold at the identity's bucket (exclusive), want false")
	}
}
