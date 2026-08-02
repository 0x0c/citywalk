package payload

import (
	"fmt"
	"testing"

	"github.com/0x0c/citywalk/internal/definition/model"
)

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

// TestTruncateKeepsEntriesUntilTheCeilingIsExceeded is CW-0002 Unit 2's size ceiling: entries arrive
// already ordered by priority, so truncation drops the tail and reports the count — a payload that
// silently omitted entries would be indistinguishable from a campaign nobody qualified for, which is
// the whole reason CW-0010 Unit 10 names a truncation metric.
func TestTruncateKeepsEntriesUntilTheCeilingIsExceeded(t *testing.T) {
	entries := []Entry{
		{MessageID: "m1", Content: make([]byte, 40)},
		{MessageID: "m2", Content: make([]byte, 40)},
		{MessageID: "m3", Content: make([]byte, 40)},
	}

	kept, dropped := truncate(entries, 100)
	if len(kept) != 2 || dropped != 1 {
		t.Fatalf("truncate(3 x 40 bytes, 100) = %d kept, %d dropped, want 2 kept, 1 dropped", len(kept), dropped)
	}
	if kept[0].MessageID != "m1" || kept[1].MessageID != "m2" {
		t.Errorf("kept = %v, want the highest-priority entries first (m1, m2)", []string{kept[0].MessageID, kept[1].MessageID})
	}
}

// TestTruncateKeepsEverythingThatFitsExactly pins the boundary: the ceiling is inclusive, so a
// payload that lands exactly on it is not truncated.
func TestTruncateKeepsEverythingThatFitsExactly(t *testing.T) {
	entries := []Entry{{Content: make([]byte, 50)}, {Content: make([]byte, 50)}}

	kept, dropped := truncate(entries, 100)
	if len(kept) != 2 || dropped != 0 {
		t.Errorf("truncate(2 x 50 bytes, 100) = %d kept, %d dropped, want 2 kept, 0 dropped", len(kept), dropped)
	}
}

// TestTruncateDropsEverythingWhenTheFirstEntryAlreadyExceedsTheCeiling covers the case with no
// partial answer available: a single oversized entry cannot be trimmed, so the payload comes back
// empty rather than over the ceiling.
func TestTruncateDropsEverythingWhenTheFirstEntryAlreadyExceedsTheCeiling(t *testing.T) {
	entries := []Entry{{Content: make([]byte, 200)}, {Content: make([]byte, 10)}}

	kept, dropped := truncate(entries, 100)
	if len(kept) != 0 || dropped != 2 {
		t.Errorf("truncate = %d kept, %d dropped, want 0 kept, 2 dropped", len(kept), dropped)
	}
}

// TestTruncateTreatsANonPositiveCeilingAsNoCeiling keeps an unconfigured deployment from serving
// empty payloads: a zero ceiling means "not configured", not "nothing fits".
func TestTruncateTreatsANonPositiveCeilingAsNoCeiling(t *testing.T) {
	entries := []Entry{{Content: make([]byte, 5_000)}}

	for _, ceiling := range []int{0, -1} {
		kept, dropped := truncate(entries, ceiling)
		if len(kept) != 1 || dropped != 0 {
			t.Errorf("truncate(_, %d) = %d kept, %d dropped, want everything kept", ceiling, len(kept), dropped)
		}
	}
}

// TestVariantsForLanguageSelectsOnlyExactMatches is the translation half of CW-0003 Unit 1's
// one-mechanism-for-both design: language selection happens before the experiment split, so only the
// device's own language group reaches the range table.
func TestVariantsForLanguageSelectsOnlyExactMatches(t *testing.T) {
	variants := []model.Variant{
		{ID: "ja-a", Language: "ja"},
		{ID: "en-a", Language: "en"},
		{ID: "ja-b", Language: "ja"},
	}

	got := variantsForLanguage(variants, "ja")
	if len(got) != 2 || got[0].ID != "ja-a" || got[1].ID != "ja-b" {
		t.Errorf("variantsForLanguage(_, \"ja\") = %v, want [ja-a ja-b]", variantIDs(got))
	}
	if got := variantsForLanguage(variants, "fr"); len(got) != 0 {
		t.Errorf("variantsForLanguage(_, \"fr\") = %v, want none", variantIDs(got))
	}
}

// TestVariantsSupportingMajorFiltersOnMajorAlone is CW-0003 Unit 4's parallel-emission rule applied
// at assembly: a device only ever receives content whose Major it declared, while a Minor it has
// never heard of stays renderable because Minor changes are additive.
func TestVariantsSupportingMajorFiltersOnMajorAlone(t *testing.T) {
	variants := []model.Variant{
		{ID: "v1", SchemaVersion: model.SchemaVersion{Major: 1, Minor: 0}},
		{ID: "v1-newer-minor", SchemaVersion: model.SchemaVersion{Major: 1, Minor: 9}},
		{ID: "v2", SchemaVersion: model.SchemaVersion{Major: 2, Minor: 0}},
	}

	got := variantsSupportingMajor(variants, 1)
	if len(got) != 2 || got[0].ID != "v1" || got[1].ID != "v1-newer-minor" {
		t.Errorf("variantsSupportingMajor(_, 1) = %v, want [v1 v1-newer-minor]", variantIDs(got))
	}
	if got := variantsSupportingMajor(variants, 3); len(got) != 0 {
		t.Errorf("variantsSupportingMajor(_, 3) = %v, want none", variantIDs(got))
	}
}

// TestAssignVariantIsStableForTheSameIdentity is CW-0008 Unit 2's core promise: a device must see the
// same arm on every synchronization, or an experiment measures nothing. The variants are deliberately
// passed in a different order on the second call, since the database's row order is not a stable
// input and the range table must not depend on it.
func TestAssignVariantIsStableForTheSameIdentity(t *testing.T) {
	msg := model.Message{ID: "m1", ExperimentSalt: "salt-1"}
	variants := []model.Variant{{ID: "v-a", Weight: 50}, {ID: "v-b", Weight: 50}}
	reversed := []model.Variant{variants[1], variants[0]}

	first, holdout, err := assignVariant(msg, variants, "channel-1")
	if err != nil {
		t.Fatalf("assignVariant: %v", err)
	}
	if holdout {
		t.Fatal("assignVariant: got holdout=true for a message with no holdout reserved")
	}

	for i := 0; i < 10; i++ {
		again, _, err := assignVariant(msg, reversed, "channel-1")
		if err != nil {
			t.Fatalf("assignVariant: %v", err)
		}
		if again.ID != first.ID {
			t.Fatalf("assignVariant returned %q then %q for the same identity, want a stable arm", first.ID, again.ID)
		}
	}
}

// TestAssignVariantSplitsAPopulationAcrossBothArms guards against a table that technically returns a
// stable answer but hands everyone the same arm — which would also pass the stability test above.
func TestAssignVariantSplitsAPopulationAcrossBothArms(t *testing.T) {
	msg := model.Message{ID: "m1", ExperimentSalt: "salt-1"}
	variants := []model.Variant{{ID: "v-a", Weight: 50}, {ID: "v-b", Weight: 50}}

	counts := map[string]int{}
	for i := 0; i < 1_000; i++ {
		v, _, err := assignVariant(msg, variants, fmt.Sprintf("channel-%d", i))
		if err != nil {
			t.Fatalf("assignVariant: %v", err)
		}
		counts[v.ID]++
	}
	if counts["v-a"] == 0 || counts["v-b"] == 0 {
		t.Errorf("assignment counts = %v, want both arms represented across 1000 identities", counts)
	}
}

// TestAssignVariantReservesTheHoldoutFraction is CW-0008 Unit 4: a fully reserved holdout means no
// identity is ever assigned an arm, and the caller learns it is a holdout rather than receiving a
// zero-valued variant it might try to render.
func TestAssignVariantReservesTheHoldoutFraction(t *testing.T) {
	msg := model.Message{ID: "m1", ExperimentSalt: "salt-1", HoldoutFraction: 1}
	variants := []model.Variant{{ID: "v-a", Weight: 100}}

	v, holdout, err := assignVariant(msg, variants, "channel-1")
	if err != nil {
		t.Fatalf("assignVariant: %v", err)
	}
	if !holdout {
		t.Fatalf("assignVariant with HoldoutFraction=1 returned variant %q, want a holdout", v.ID)
	}
	if v.ID != "" {
		t.Errorf("assignVariant returned variant %q alongside holdout=true, want the zero variant", v.ID)
	}
}

// TestAssignVariantRejectsAnEmptyLanguageGroup keeps an unassignable message from silently yielding
// the zero variant: with no arms and no holdout, there is no range table to build, and returning a
// blank variant would put an entry with no content into the payload.
func TestAssignVariantRejectsAnEmptyLanguageGroup(t *testing.T) {
	msg := model.Message{ID: "m1", ExperimentSalt: "salt-1"}

	if _, _, err := assignVariant(msg, nil, "channel-1"); err == nil {
		t.Fatal("assignVariant with no variants and no holdout: got nil error, want a rejection")
	}
}

func variantIDs(variants []model.Variant) []string {
	ids := make([]string, len(variants))
	for i, v := range variants {
		ids[i] = v.ID
	}
	return ids
}
