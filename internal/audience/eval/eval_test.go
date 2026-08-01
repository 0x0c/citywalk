package eval_test

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/audience/predicate"
)

func compile(t *testing.T, source string) *predicate.Predicate {
	t.Helper()
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	p, err := predicate.Compile(env, audiencetest.Registry(), source)
	if err != nil {
		t.Fatalf("Compile(%q): %v", source, err)
	}
	return p
}

func TestEvaluateSimpleComparison(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	e := eval.New(env)

	p := compile(t, `country == "JP" && total_distance_km >= 100.0`)

	matched, err := e.Evaluate(p, map[string]any{
		"country": "JP", "total_distance_km": 150.0,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !matched {
		t.Error("Evaluate: got false, want true")
	}

	matched, err = e.Evaluate(p, map[string]any{
		"country": "US", "total_distance_km": 150.0,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if matched {
		t.Error("Evaluate: got true, want false")
	}
}

func TestEvaluateSemVerOrdersCorrectly(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	e := eval.New(env)

	p := compile(t, `semver(app_version) >= semver("2.10.0")`)

	// 2.9.0 is lower than 2.10.0 numerically, though "2.9.0" > "2.10.0" lexically — this is the
	// exact case CW-0004 Unit 1 exists to get right.
	matched, err := e.Evaluate(p, map[string]any{"app_version": "2.9.0"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if matched {
		t.Error("Evaluate(app_version=2.9.0): got true, want false (2.9.0 < 2.10.0)")
	}

	matched, err = e.Evaluate(p, map[string]any{"app_version": "2.10.1"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !matched {
		t.Error("Evaluate(app_version=2.10.1): got false, want true (2.10.1 >= 2.10.0)")
	}
}

func TestEvaluateStringSetMembership(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	e := eval.New(env)

	p := compile(t, `"riverside_loop" in favorite_routes`)

	matched, err := e.Evaluate(p, map[string]any{
		"favorite_routes": []string{"riverside_loop", "harbor_walk"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !matched {
		t.Error("Evaluate: got false, want true")
	}

	matched, err = e.Evaluate(p, map[string]any{
		"favorite_routes": []string{"harbor_walk"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if matched {
		t.Error("Evaluate: got true, want false")
	}
}

func TestEvaluateTimestampComparison(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	e := eval.New(env)

	p := compile(t, `registered_at < timestamp("2026-01-01T00:00:00Z")`)

	matched, err := e.Evaluate(p, map[string]any{
		"registered_at": time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !matched {
		t.Error("Evaluate: got false, want true")
	}
}

func TestEvaluateRepeatedlyAgreesWithSingleEvaluation(t *testing.T) {
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	e := eval.New(env)
	p := compile(t, `country == "JP"`)

	for i, country := range []string{"JP", "US", "JP"} {
		matched, err := e.Evaluate(p, map[string]any{"country": country})
		if err != nil {
			t.Fatalf("Evaluate[%d]: %v", i, err)
		}
		want := country == "JP"
		if matched != want {
			t.Errorf("Evaluate[%d](country=%q) = %v, want %v", i, country, matched, want)
		}
	}
}
