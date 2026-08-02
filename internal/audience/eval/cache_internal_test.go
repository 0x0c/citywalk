package eval

import (
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/predicate"
)

// TestProgramCacheReusesEntryForSameHash is a white-box test of the cache CW-0004 Unit 3 requires:
// two predicates compiled from identical source share one cached *cel.Program, keyed by content
// hash, rather than each Evaluate call recompiling.
func TestProgramCacheReusesEntryForSameHash(t *testing.T) {
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

	e := New(env)
	if _, err := e.Evaluate(a, map[string]any{"country": "JP"}); err != nil {
		t.Fatalf("Evaluate(a): %v", err)
	}
	if _, err := e.Evaluate(b, map[string]any{"country": "JP"}); err != nil {
		t.Fatalf("Evaluate(b): %v", err)
	}

	e.mu.RLock()
	cacheSize := len(e.programs)
	e.mu.RUnlock()
	if cacheSize != 1 {
		t.Errorf("program cache size = %d, want 1 (a and b share a hash)", cacheSize)
	}
}
