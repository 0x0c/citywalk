package predicate_test

import (
	"strings"
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/predicate"
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
