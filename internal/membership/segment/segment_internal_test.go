package segment

import (
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
)

// classify compiles source and reports the refresh mode Save would have derived from it, without the
// database write Save also performs — the classification is the part with a rule behind it, and it is
// pure.
func classify(t *testing.T, source string) RefreshMode {
	t.Helper()
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, source)
	if err != nil {
		t.Fatalf("Compile(%q): %v", source, err)
	}
	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if referencesEventAggregate(ast.NativeRep(), reg) {
		return RefreshBatchOnly
	}
	return RefreshIncremental
}

// TestPredicatesOverStoredAttributesAreIncremental is the common case: every value the predicate
// reads is written into channels.attributes, so an attribute write is the only thing that can change
// membership — exactly the signal CW-0005 Unit 5's incremental path is driven by.
func TestPredicatesOverStoredAttributesAreIncremental(t *testing.T) {
	sources := []string{
		`country == "JP"`,
		`is_premium && total_distance_km > 100.0`,
		`semver(app_version) >= semver("2.9.0")`,
		`"kamakura" in favorite_routes`,
	}
	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			if got := classify(t, source); got != RefreshIncremental {
				t.Errorf("refresh mode for %q = %q, want %q", source, got, RefreshIncremental)
			}
		})
	}
}

// TestPredicatesTouchingAnEventAggregateAreBatchOnly is the exclusion CW-0005 Unit 5 names: a
// trailing-window rollup moves as days roll over, with no attribute write anywhere to trigger a
// re-evaluation. Classifying such a segment as incremental would leave it permanently stale between
// batches, so a single event-aggregate reference anywhere in the tree — even alongside ordinary
// attributes, and even under a negation — has to disqualify the whole predicate.
func TestPredicatesTouchingAnEventAggregateAreBatchOnly(t *testing.T) {
	sources := []string{
		`route_screen_views_7d >= 3.0`,
		`country == "JP" && route_screen_views_7d >= 3.0`,
		`!(route_screen_views_7d >= 3.0)`,
		`is_premium || route_screen_views_7d >= 1.0`,
	}
	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			if got := classify(t, source); got != RefreshBatchOnly {
				t.Errorf("refresh mode for %q = %q, want %q", source, got, RefreshBatchOnly)
			}
		})
	}
}

// TestClassificationFollowsTheRegistryNotTheAttributeName guards against the classifier keying off
// anything about the identifier itself: what makes an attribute batch-only is its registered Source,
// so the same predicate text classifies differently against a registry that sources the name
// differently.
func TestClassificationFollowsTheRegistryNotTheAttributeName(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	p, err := predicate.Compile(env, audiencetest.Registry(), `route_screen_views_7d >= 3.0`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The same name, re-registered as a plain stored user attribute: nothing about the tree changed,
	// so a classifier that pattern-matched the identifier would still say batch-only.
	restored, err := registry.New(
		registry.Definition{Name: "route_screen_views_7d", Type: registry.TypeNumber, Source: registry.SourceUserAttribute},
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	if referencesEventAggregate(ast.NativeRep(), restored) {
		t.Error("referencesEventAggregate = true against a registry that sources the name as a user attribute, want false")
	}
}
