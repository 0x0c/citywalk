package incremental_test

import (
	"sort"
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
	"github.com/0x0c/citywalk/internal/membership/incremental"
	"github.com/0x0c/citywalk/internal/membership/segment"
)

// newSegment builds an in-memory segment carrying a real compiled predicate tree, since DependencyMap
// reads the tree rather than the source text — a hand-written struct with only Source set would not
// exercise anything.
func newSegment(t *testing.T, id, source string, mode segment.RefreshMode) *segment.Segment {
	t.Helper()
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	p, err := predicate.Compile(env, audiencetest.Registry(), source)
	if err != nil {
		t.Fatalf("Compile(%q): %v", source, err)
	}
	return &segment.Segment{ID: id, Name: id, Predicate: p, RefreshMode: mode}
}

func segmentIDs(segments []*segment.Segment) []string {
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}
	sort.Strings(ids)
	return ids
}

// TestDependencyMapNamesEveryAttributeASegmentReferences is what bounds CW-0005 Unit 5's incremental
// work: a change to one attribute re-evaluates only the segments that actually read it. A segment
// missing from an attribute's list would go stale until the next full batch, so every referenced
// attribute has to name the segment — including both sides of a compound predicate.
func TestDependencyMapNamesEveryAttributeASegmentReferences(t *testing.T) {
	compound := newSegment(t, "seg-compound", `country == "JP" && is_premium`, segment.RefreshIncremental)
	single := newSegment(t, "seg-single", `country == "US"`, segment.RefreshIncremental)

	deps, err := incremental.DependencyMap([]*segment.Segment{compound, single}, audiencetest.Registry())
	if err != nil {
		t.Fatalf("DependencyMap: %v", err)
	}

	if got, want := segmentIDs(deps["country"]), []string{"seg-compound", "seg-single"}; !equalStrings(got, want) {
		t.Errorf("deps[\"country\"] = %v, want %v", got, want)
	}
	if got, want := segmentIDs(deps["is_premium"]), []string{"seg-compound"}; !equalStrings(got, want) {
		t.Errorf("deps[\"is_premium\"] = %v, want %v", got, want)
	}
	if segs, ok := deps["total_distance_km"]; ok {
		t.Errorf("deps[\"total_distance_km\"] = %v, want no entry for an attribute nothing references", segmentIDs(segs))
	}
}

// TestDependencyMapExcludesBatchOnlySegments is Unit 5's eligibility rule stated as a test: a
// segment whose predicate reads an event aggregate can change membership with the passage of time
// alone, which no attribute write signals — so it must never appear in the map an attribute change
// drives, even for the ordinary attributes it also happens to reference.
func TestDependencyMapExcludesBatchOnlySegments(t *testing.T) {
	batchOnly := newSegment(t, "seg-batch", `country == "JP" && route_screen_views_7d >= 3.0`, segment.RefreshBatchOnly)

	deps, err := incremental.DependencyMap([]*segment.Segment{batchOnly}, audiencetest.Registry())
	if err != nil {
		t.Fatalf("DependencyMap: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DependencyMap = %v, want no entries for a batch-only segment", deps)
	}
}

// TestDependencyMapSkipsIdentifiersTheRegistryDoesNotDescribe covers the registry filter: a segment
// saved against an older registry can reference a name since retired, and keying the map on it would
// build a dependency no attribute write can ever match.
func TestDependencyMapSkipsIdentifiersTheRegistryDoesNotDescribe(t *testing.T) {
	seg := newSegment(t, "seg-1", `country == "JP" && is_premium`, segment.RefreshIncremental)

	// A registry that has since dropped is_premium: country still resolves, is_premium must not.
	narrowed, err := registry.New(
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	deps, err := incremental.DependencyMap([]*segment.Segment{seg}, narrowed)
	if err != nil {
		t.Fatalf("DependencyMap: %v", err)
	}
	if len(deps["country"]) != 1 {
		t.Errorf("deps[\"country\"] = %v, want the segment listed", segmentIDs(deps["country"]))
	}
	if _, ok := deps["is_premium"]; ok {
		t.Error(`deps["is_premium"]: got an entry, want none for a name the registry no longer describes`)
	}
}

// TestDependencyMapReportsAnUndecodablePredicateTree keeps a corrupt stored tree from silently
// yielding an empty dependency list, which would look exactly like a segment that references nothing
// and quietly stop being maintained.
func TestDependencyMapReportsAnUndecodablePredicateTree(t *testing.T) {
	seg := &segment.Segment{
		ID:          "seg-corrupt",
		Predicate:   &predicate.Predicate{Source: `country == "JP"`, Tree: []byte("not a checked expression")},
		RefreshMode: segment.RefreshIncremental,
	}

	if _, err := incremental.DependencyMap([]*segment.Segment{seg}, audiencetest.Registry()); err == nil {
		t.Fatal("DependencyMap: got nil error for an undecodable predicate tree, want an error")
	}
}

func TestDependencyMapOnNoSegmentsIsEmpty(t *testing.T) {
	deps, err := incremental.DependencyMap(nil, audiencetest.Registry())
	if err != nil {
		t.Fatalf("DependencyMap: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DependencyMap(nil) = %v, want an empty map", deps)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
