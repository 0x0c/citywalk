package payload

import "testing"

func TestAllocateExperimentBucketsSumsToTotal(t *testing.T) {
	ids := []string{"a", "b", "c"}
	weights := map[string]int{"a": 33, "b": 33, "c": 34}
	buckets := allocateExperimentBuckets(ids, weights, 10000)

	sum := 0
	for _, id := range ids {
		sum += buckets[id]
	}
	if sum != 10000 {
		t.Errorf("sum of allocated buckets = %d, want 10000", sum)
	}
}

func TestAllocateExperimentBucketsEvenSplit(t *testing.T) {
	ids := []string{"a", "b"}
	weights := map[string]int{"a": 50, "b": 50}
	buckets := allocateExperimentBuckets(ids, weights, 10000)

	if buckets["a"] != 5000 || buckets["b"] != 5000 {
		t.Errorf("buckets = %v, want a=5000 b=5000", buckets)
	}
}

// TestAllocateExperimentBucketsIsDeterministic is CW-0008's whole premise applied to this
// allocation step: the same weights, computed twice, must produce byte-identical results, since two
// servers must agree on the range table without communicating.
func TestAllocateExperimentBucketsIsDeterministic(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	weights := map[string]int{"a": 10, "b": 15, "c": 25, "d": 50}

	first := allocateExperimentBuckets(ids, weights, 9973) // an odd total, to force remainders
	for i := 0; i < 20; i++ {
		again := allocateExperimentBuckets(ids, weights, 9973)
		for _, id := range ids {
			if again[id] != first[id] {
				t.Fatalf("run %d: buckets[%q] = %d, want %d (first run)", i, id, again[id], first[id])
			}
		}
	}
}

// TestAllocateExperimentBucketsBreaksTiesByID confirms the documented tie-break rule directly: two
// variants with identical weights (and therefore identical remainders) split leftover buckets by
// variant ID, not by input order or map iteration order.
func TestAllocateExperimentBucketsBreaksTiesByID(t *testing.T) {
	ids := []string{"z-variant", "a-variant"}
	weights := map[string]int{"z-variant": 50, "a-variant": 50}
	// 10001 forces exactly one leftover bucket after flooring (5000 + 5000 = 10000, 1 left over).
	buckets := allocateExperimentBuckets(ids, weights, 10001)

	if buckets["a-variant"] != 5001 || buckets["z-variant"] != 5000 {
		t.Errorf("buckets = %v, want a-variant=5001 (tie broken by lexically smaller id) z-variant=5000", buckets)
	}
}

func TestAllocateExperimentBucketsHandlesUnevenWeights(t *testing.T) {
	ids := []string{"a", "b", "c"}
	weights := map[string]int{"a": 1, "b": 1, "c": 98}
	buckets := allocateExperimentBuckets(ids, weights, 10000)

	sum := 0
	for _, id := range ids {
		sum += buckets[id]
	}
	if sum != 10000 {
		t.Errorf("sum = %d, want 10000", sum)
	}
	if buckets["c"] <= buckets["a"] || buckets["c"] <= buckets["b"] {
		t.Errorf("buckets = %v, want c's allocation to dominate a and b's", buckets)
	}
}
