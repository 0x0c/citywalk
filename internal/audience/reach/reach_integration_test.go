//go:build integration

// Run with: go test -tags=integration ./internal/audience/reach/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package reach_test

import (
	"context"
	"os"
	"testing"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/reach"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

// TestEstimateReachOnAKnownPopulation seeds a population where exactly a known fraction matches and
// checks the estimate against real Postgres sampling. It intentionally does not assert that the
// returned 95%-confidence interval contains the true count: at a real 95% confidence level, that
// assertion is expected to fail on roughly one run in twenty by construction, which is exactly the
// kind of test that must never depend on an outcome the code does not guarantee outright. The
// interval math itself (does the formula compute a correct 95% interval) is covered instead by the
// deterministic wilson_internal_test.go, which needs no sampling and cannot flake. What this test
// checks is a bound wide enough that a real bug (sampling from the wrong table, an inverted
// predicate, a broken proportion calculation) would still be caught, while pure sampling variance at
// n=500 essentially never crosses it.
func TestEstimateReachOnAKnownPopulation(t *testing.T) {
	dsn := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM conversion_attributions`); err != nil {
		t.Fatalf("clear conversion_attributions: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM events_log`); err != nil {
		t.Fatalf("clear events_log: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM channels`); err != nil {
		t.Fatalf("clear channels: %v", err)
	}

	const total = 1000
	const matchingFraction = 0.3 // exactly 300 of 1000 channels are "JP"
	matchingCount := int(total * matchingFraction)
	for i := 0; i < total; i++ {
		country := "US"
		if i < matchingCount {
			country = "JP"
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO channels (attributes) VALUES ($1)`,
			map[string]any{"country": country},
		); err != nil {
			t.Fatalf("insert channel %d: %v", i, err)
		}
	}

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, `country == "JP"`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	evaluator := eval.New(env)

	est, err := reach.EstimateReach(ctx, pool, evaluator, reg, p, 500)
	if err != nil {
		t.Fatalf("EstimateReach: %v", err)
	}

	if est.PopulationSize != total {
		t.Errorf("PopulationSize = %d, want %d", est.PopulationSize, total)
	}
	if est.SampleSize != 500 {
		t.Errorf("SampleSize = %d, want 500", est.SampleSize)
	}
	// Low <= Count <= High is guaranteed by construction (wilsonInterval always brackets its own
	// center), so this checks the wiring rather than the statistics.
	if est.Low > est.Count || est.Count > est.High {
		t.Errorf("interval [%d, %d] does not contain Count %d", est.Low, est.High, est.Count)
	}

	// A generous sanity bound, not the 95% CI: at n=500 sampled from a true 30% proportion, the
	// sampling standard deviation is about sqrt(500*0.3*0.7) ~= 10.2 in sample-count terms, roughly
	// 2% of the population once scaled up. +/-20% of the true count is about ten standard
	// deviations — a real bug shows up here reliably, while sampling noise essentially never does.
	tolerance := int(0.2 * float64(matchingCount))
	if diff := est.Count - matchingCount; diff > tolerance || diff < -tolerance {
		t.Errorf("Count = %d, want within %d of the true count %d (got %+d)", est.Count, tolerance, matchingCount, diff)
	}
}

// TestEstimateReachOnEmptyPopulation demonstrates EstimateReach reports zero cleanly rather than
// dividing by zero when no channel exists yet.
func TestEstimateReachOnEmptyPopulation(t *testing.T) {
	dsn := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM conversion_attributions`); err != nil {
		t.Fatalf("clear conversion_attributions: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM events_log`); err != nil {
		t.Fatalf("clear events_log: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM channels`); err != nil {
		t.Fatalf("clear channels: %v", err)
	}

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	p, err := predicate.Compile(env, reg, `country == "JP"`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	evaluator := eval.New(env)

	est, err := reach.EstimateReach(ctx, pool, evaluator, reg, p, 500)
	if err != nil {
		t.Fatalf("EstimateReach: %v", err)
	}
	if est != (reach.Estimate{}) {
		t.Errorf("Estimate = %+v, want the zero value for an empty population", est)
	}
}
