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

// TestEstimateReachOnAKnownPopulation seeds a population where exactly a known fraction matches,
// and checks the estimate's confidence interval actually contains the true count — the property
// CW-0004 Unit 6 exists to give an author, rather than a bare number with no sense of its error.
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
	if est.Low > est.Count || est.Count > est.High {
		t.Errorf("interval [%d, %d] does not contain Count %d", est.Low, est.High, est.Count)
	}
	if est.Low > matchingCount || matchingCount > est.High {
		t.Errorf("interval [%d, %d] does not contain the true count %d", est.Low, est.High, matchingCount)
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
