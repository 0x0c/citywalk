//go:build integration

// Run with: go test -tags=integration ./internal/event/consumer/... with
// CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package consumer_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/event/consumer"
	"github.com/0x0c/citywalk/internal/event/ingest"
	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/event/ratelimit"
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

	for _, table := range []string{"targeting_rollup", "event_consumer_offsets", "events_log", "channels"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	return pool, redisClient
}

// TestRollupRecomputeWorkerWorkInvokesRunOnce proves RollupRecomputeWorker.Work actually drains
// events_log into the targeting rollup (CW-0009 Unit 1's scheduled recomputation), not just that it
// compiles: an event is accepted but never consumed, then Work runs once, and the targeting rollup
// reflects it — exactly what only a real RunOnce call produces.
func TestRollupRecomputeWorkerWorkInvokesRunOnce(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		{ID: "01912d2c-0000-7000-8000-000000000001", ChannelID: channelID, Kind: model.KindCustom, Name: "screen_view", DeviceTime: now},
	}
	if _, err := ingest.Accept(ctx, pool, limiter, channelID, events, now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM targeting_rollup`).Scan(&before); err != nil {
		t.Fatalf("count targeting_rollup (before): %v", err)
	}
	if before != 0 {
		t.Fatalf("targeting_rollup count before Work = %d, want 0", before)
	}

	worker := &consumer.RollupRecomputeWorker{Pool: pool}
	job := &river.Job[consumer.RollupRecomputeArgs]{Args: consumer.RollupRecomputeArgs{}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("Work: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count FROM targeting_rollup WHERE channel_id = $1 AND event_name = 'screen_view'`, channelID,
	).Scan(&count); err != nil {
		t.Fatalf("query targeting_rollup: %v", err)
	}
	if count != 1 {
		t.Errorf("targeting_rollup count for screen_view = %d, want 1", count)
	}
}
