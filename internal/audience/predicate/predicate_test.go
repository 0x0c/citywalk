package predicate_test

import (
	"strings"
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
)

func TestCompileAndLoadRoundTrip(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	p, err := predicate.Compile(env, reg, `country == "JP" && total_distance_km >= 100.0`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.Hash == "" {
		t.Error("Hash is empty")
	}
	if p.Source != `country == "JP" && total_distance_km >= 100.0` {
		t.Errorf("Source = %q, want the original source text", p.Source)
	}

	ast, err := predicate.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ast.IsChecked() {
		t.Error("Load: round-tripped AST is not checked")
	}
}

func TestCompileTwoEqualPredicatesHashIdentically(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	a, err := predicate.Compile(env, reg, `country == "JP"`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	b, err := predicate.Compile(env, reg, `country == "JP"`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if a.Hash != b.Hash {
		t.Errorf("Hash(a) = %q, Hash(b) = %q, want them equal for identical source", a.Hash, b.Hash)
	}
}

func TestCompileRejectsUnknownAttribute(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	_, err = predicate.Compile(env, reg, `not_in_registry == "x"`)
	if err == nil {
		t.Fatal("Compile: got nil error for an unregistered attribute, want an error")
	}
}

// TestCompileRejectsBareSemVerComparison demonstrates the guard against CW-0004 Unit 1's headline
// failure mode: comparing a semver attribute as a plain string, which type-checks fine but ranks
// "2.9.0" above "2.10.0".
func TestCompileRejectsBareSemVerComparison(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	_, err = predicate.Compile(env, reg, `app_version >= "2.10.0"`)
	if err == nil {
		t.Fatal("Compile: got nil error for a bare semver comparison, want a rejection")
	}
	if !strings.Contains(err.Error(), "semver(") {
		t.Errorf("Compile error = %q, want it to suggest wrapping in semver(...)", err)
	}
}

func TestCompileAcceptsWrappedSemVerComparison(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	if _, err := predicate.Compile(env, reg, `semver(app_version) >= semver("2.10.0")`); err != nil {
		t.Fatalf("Compile: %v, want nil", err)
	}
}

// TestCompileAcceptsADayGranularityAggregateCondition proves CW-0004 Unit 5's granularity check is a
// no-op against the registry as it exists today: route_screen_views_7d is registered at
// AggregateGranularityDay, and its window is a whole number of days, so it carries exactly the
// day-level precision every windowed condition requests.
func TestCompileAcceptsADayGranularityAggregateCondition(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	if _, err := predicate.Compile(env, reg, `route_screen_views_7d >= 3.0`); err != nil {
		t.Fatalf("Compile: %v, want nil", err)
	}
}

// TestCompileRejectsAnAggregateConditionCoarserThanRequestedGranularity is CW-0004 Unit 5's
// type-checker rejection, exercised end to end: "week" does not exist as a production granularity —
// AggregateGranularityDay is the only one a real Definition uses today — but registry.GranularityCarries
// already orders it correctly, so a Definition registered against it (constructed only here, never in
// production code) stands in for the "second, finer granularity" the roadmap note describes, and
// demonstrates the condition's fixed day-level request exceeding it is rejected rather than silently
// compiled.
func TestCompileRejectsAnAggregateConditionCoarserThanRequestedGranularity(t *testing.T) {
	reg, err := registry.New(
		registry.Definition{
			Name: "route_screen_views_weekly", Type: registry.TypeNumber, Source: registry.SourceEventAggregate,
			AggregateGranularity: "week",
			AggregateEventName:   "route_screen_view", AggregateWindowDays: 7,
		},
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	env, err := predicate.BuildEnv(reg)
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}

	_, err = predicate.Compile(env, reg, `route_screen_views_weekly >= 3.0`)
	if err == nil {
		t.Fatal("Compile: got nil error for a condition against a week-granularity aggregate, want a rejection")
	}
	if !strings.Contains(err.Error(), "granularity") {
		t.Errorf("Compile error = %q, want it to name the granularity mismatch", err)
	}
}

// TestSemVerFunctionOrdersVersionsNumericallyAtEvaluation is the row-wise half of the ordering
// CW-0004 Unit 1 requires: "2.10.0" is a later version than "2.9.0", and plain lexical string
// comparison gets that backwards. The semver() wrapper an author must write is what makes the
// comparison numeric, so this test evaluates it rather than only type-checking it.
func TestSemVerFunctionOrdersVersionsNumericallyAtEvaluation(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, `semver(app_version) >= semver("2.9.0")`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	evaluator := eval.New(env)

	tests := map[string]bool{
		"2.10.0": true, // Later than 2.9.0 numerically, earlier lexically — the case the wrapper exists for.
		"2.9.0":  true, // The boundary itself.
		"2.8.9":  false,
		"2":      false, // A missing minor and patch default to 0, so this is 2.0.0.
		"10":     true,  // And a bare major still compares as a number, not a first byte.
	}

	for version, want := range tests {
		t.Run(version, func(t *testing.T) {
			got, err := evaluator.Evaluate(p, map[string]any{"app_version": version})
			if err != nil {
				t.Fatalf("Evaluate(app_version=%q): %v", version, err)
			}
			if got != want {
				t.Errorf("semver(%q) >= semver(\"2.9.0\") = %v, want %v", version, got, want)
			}
		})
	}
}

// TestSemVerFunctionSurfacesAnUnparseableAttributeValueAsAnError keeps a malformed stored version
// from evaluating to a quiet false: a channel whose app_version is not a version at all is a data
// problem the caller has to see, not a channel that simply fails the predicate.
func TestSemVerFunctionSurfacesAnUnparseableAttributeValueAsAnError(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, `semver(app_version) >= semver("2.9.0")`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if _, err := eval.New(env).Evaluate(p, map[string]any{"app_version": "nightly-build"}); err == nil {
		t.Fatal("Evaluate(app_version=\"nightly-build\"): got nil error, want the semver failure surfaced")
	}
}
