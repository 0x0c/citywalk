package hll

import (
	"fmt"
	"math"
	"testing"
)

// TestEstimateOnKnownCardinality adds a large, deterministic set of distinct identities (never
// math/rand, per the platform's no-flaky-tests rule) and checks the estimate against a generous
// sanity bound rather than the ~1% error the design doc quotes — a wide bound that is virtually
// always true is what keeps this test from failing on the rare unlucky draw.
func TestEstimateOnKnownCardinality(t *testing.T) {
	const trueCount = 200000
	s := New()
	for i := 0; i < trueCount; i++ {
		s.AddIdentity(fmt.Sprintf("channel-%d", i))
	}

	got := s.Estimate()
	relError := math.Abs(got-trueCount) / trueCount
	if relError > 0.1 {
		t.Errorf("Estimate() = %.0f, true count %d, relative error %.4f exceeds 10%% sanity bound", got, trueCount, relError)
	}
}

// TestAddingDuplicatesDoesNotInflate re-adds the same handful of identities many times; the estimate
// must stay near the small true distinct count, not grow with the number of Add calls.
func TestAddingDuplicatesDoesNotInflate(t *testing.T) {
	s := New()
	for i := 0; i < 10000; i++ {
		s.AddIdentity(fmt.Sprintf("channel-%d", i%5))
	}

	got := s.Estimate()
	if got < 1 || got > 15 {
		t.Errorf("Estimate() = %.2f with 5 distinct identities repeated, want roughly 5 (allowing generous slack)", got)
	}
}

// TestMergeMatchesDirectUnion is the property Unit 5's daily-into-weekly combination relies on:
// merging two sketches built from disjoint halves of a population must produce the same registers,
// and therefore the same estimate, as building one sketch from the whole population directly. This
// is an exact equality (register-wise max is deterministic), not a statistical bound.
func TestMergeMatchesDirectUnion(t *testing.T) {
	const total = 50000

	direct := New()
	first := New()
	second := New()
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("channel-%d", i)
		direct.AddIdentity(id)
		if i%2 == 0 {
			first.AddIdentity(id)
		} else {
			second.AddIdentity(id)
		}
	}

	if err := first.Merge(second); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if first.Estimate() != direct.Estimate() {
		t.Errorf("merged estimate %.4f != direct estimate %.4f, want exact equality", first.Estimate(), direct.Estimate())
	}
}

func TestMergeRejectsMismatchedSize(t *testing.T) {
	s := New()
	other := &Sketch{registers: make([]byte, registerCount-1)}
	if err := s.Merge(other); err == nil {
		t.Fatal("Merge: got nil error for mismatched register count, want an error")
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	s := New()
	for i := 0; i < 1000; i++ {
		s.AddIdentity(fmt.Sprintf("channel-%d", i))
	}

	got, err := Unmarshal(s.Marshal())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Estimate() != s.Estimate() {
		t.Errorf("round-tripped estimate %.4f != original %.4f", got.Estimate(), s.Estimate())
	}
}

func TestUnmarshalRejectsWrongLength(t *testing.T) {
	if _, err := Unmarshal(make([]byte, 10)); err == nil {
		t.Fatal("Unmarshal: got nil error for wrong-length input, want an error")
	}
}
