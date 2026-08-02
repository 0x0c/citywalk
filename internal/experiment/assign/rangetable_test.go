package assign_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/experiment/assign"
)

func TestNewRangeTableCoversTheWholeSpace(t *testing.T) {
	table, err := assign.NewRangeTable(
		[]string{"A", "B"},
		map[string]int{"A": 5000, "B": 4000},
		1000,
	)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}

	for bucket, want := range map[int]string{0: "A", 4999: "A", 5000: "B", 8999: "B"} {
		variant, isHoldout, err := table.Lookup(bucket)
		if err != nil {
			t.Fatalf("Lookup(%d): %v", bucket, err)
		}
		if variant != want || isHoldout {
			t.Errorf("Lookup(%d) = (%q, holdout=%v), want (%q, holdout=false)", bucket, variant, isHoldout, want)
		}
	}
	_, isHoldout, err := table.Lookup(9500)
	if err != nil {
		t.Fatalf("Lookup(9500): %v", err)
	}
	if !isHoldout {
		t.Error("Lookup(9500) is not in the holdout, want it to be (buckets 9000-9999 reserved)")
	}
}

func TestNewRangeTableRejectsWeightsNotSummingToBucketCount(t *testing.T) {
	_, err := assign.NewRangeTable([]string{"A", "B"}, map[string]int{"A": 5000, "B": 4000}, 500)
	if err == nil {
		t.Fatal("NewRangeTable: got nil error for weights summing to 9500, want an error")
	}
}

func TestNewRangeTableRejectsAMissingWeight(t *testing.T) {
	_, err := assign.NewRangeTable([]string{"A", "B"}, map[string]int{"A": 10000}, 0)
	if err == nil {
		t.Fatal("NewRangeTable: got nil error for a variant with no weight, want an error")
	}
}

func TestLookupRejectsAnOutOfRangeBucket(t *testing.T) {
	table, err := assign.NewRangeTable([]string{"A"}, map[string]int{"A": 10000}, 0)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}
	if _, _, err := table.Lookup(assign.BucketCount); err == nil {
		t.Fatal("Lookup(BucketCount): got nil error for a bucket past the table's range, want an error")
	}
}

// TestReweightMovesOnlyTheBucketsThatMustMove is CW-0008 Unit 3's central property: shifting a
// split from 50/50 to 60/40 must not move a bucket out of the arm it was already in, wherever the
// new proportions still allow it to stay.
func TestReweightMovesOnlyTheBucketsThatMustMove(t *testing.T) {
	before, err := assign.NewRangeTable([]string{"A", "B"}, map[string]int{"A": 5000, "B": 5000}, 0)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}

	after, err := assign.Reweight(before, map[string]int{"A": 6000, "B": 4000}, 0)
	if err != nil {
		t.Fatalf("Reweight: %v", err)
	}

	const sampleSize = assign.BucketCount
	moved := 0
	for bucket := 0; bucket < sampleSize; bucket++ {
		beforeVariant, _, err := before.Lookup(bucket)
		if err != nil {
			t.Fatalf("before.Lookup(%d): %v", bucket, err)
		}
		afterVariant, _, err := after.Lookup(bucket)
		if err != nil {
			t.Fatalf("after.Lookup(%d): %v", bucket, err)
		}
		if beforeVariant != afterVariant {
			moved++
			// Every moved bucket must have moved from B to A (the arm that grew), and must be one
			// of the 1000 buckets the 10-percentage-point shift requires (buckets 5000-5999, which
			// A's new range [0,6000) now covers but its old range [0,5000) did not).
			if beforeVariant != "B" || afterVariant != "A" {
				t.Errorf("bucket %d moved from %q to %q, want only B-to-A moves", bucket, beforeVariant, afterVariant)
			}
			if bucket < 5000 || bucket >= 6000 {
				t.Errorf("bucket %d moved but is outside the expected [5000, 6000) band", bucket)
			}
		}
	}
	if moved != 1000 {
		t.Errorf("moved %d buckets, want exactly 1000 (the width of a 10-percentage-point shift)", moved)
	}
}

func TestReweightRejectsANewVariant(t *testing.T) {
	before, err := assign.NewRangeTable([]string{"A", "B"}, map[string]int{"A": 5000, "B": 5000}, 0)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}
	if _, err := assign.Reweight(before, map[string]int{"A": 5000, "C": 5000}, 0); err == nil {
		t.Fatal("Reweight: got nil error for a weight map naming a variant not in the table, want an error")
	}
}
