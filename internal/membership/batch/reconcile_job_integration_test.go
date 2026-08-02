//go:build integration

// Run with: go test -tags=integration ./internal/membership/batch/... with
// CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package batch_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/membership/forward"
	"github.com/0x0c/citywalk/internal/membership/ordinal"
	"github.com/0x0c/citywalk/internal/membership/segment"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/internal/platform/redisclient"
	"github.com/0x0c/citywalk/migrations"
)

func testDeps(t *testing.T) (*pgxpool.Pool, *redis.Client) {
	t.Helper()
	pgDSN := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("CITYWALK_TEST_REDIS_ADDR")
	if pgDSN == "" || redisAddr == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR must both be set")
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, pgDSN)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	redisClient, err := redisclient.New(ctx, redisAddr)
	if err != nil {
		t.Fatalf("redisclient.New: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })

	for _, table := range []string{
		"conversion_attributions", "events_log", "segment_membership", "segments", "channel_ordinals", "channels",
	} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE membership_generation SET generation = 0`); err != nil {
		t.Fatalf("reset generation: %v", err)
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	return pool, redisClient
}

// TestReconcileWorkerWorkInvokesRecompute proves ReconcileWorker.Work actually calls into
// batch.Recompute (CW-0005 Unit 6's scheduled reconciliation), not just that it compiles: a channel
// and a segment it matches exist beforehand, and after Work runs, the generation pointer has
// advanced and the forward index reflects the match — exactly what only a real Recompute call
// produces.
func TestReconcileWorkerWorkInvokesRecompute(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()

	var channelID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO channels (attributes) VALUES ($1) RETURNING id`, map[string]any{"country": "JP"},
	).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := ordinal.Allocate(ctx, pool, channelID); err != nil {
		t.Fatalf("allocate ordinal: %v", err)
	}

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	jpSegment, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	before, err := forward.CurrentGeneration(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentGeneration (before): %v", err)
	}

	worker := &batch.ReconcileWorker{Pool: pool, Redis: redisClient, Registry: reg}
	job := &river.Job[batch.ReconcileArgs]{Args: batch.ReconcileArgs{}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("Work: %v", err)
	}

	after, err := forward.CurrentGeneration(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentGeneration (after): %v", err)
	}
	if after != before+1 {
		t.Errorf("generation after Work = %d, want %d (one recomputation)", after, before+1)
	}

	bitmap, err := forward.CurrentBitmap(ctx, pool, jpSegment.ID)
	if err != nil {
		t.Fatalf("CurrentBitmap: %v", err)
	}
	jpOrdinal, err := ordinal.Lookup(ctx, pool, channelID)
	if err != nil {
		t.Fatalf("Lookup ordinal: %v", err)
	}
	if !bitmap.Contains(uint32(jpOrdinal)) {
		t.Error("forward index for the Japan segment does not contain the matching channel after Work")
	}
}
