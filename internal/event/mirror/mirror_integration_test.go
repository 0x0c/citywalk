//go:build integration

// Run with: go test -tags=integration ./internal/event/mirror/... with CITYWALK_TEST_POSTGRES_DSN,
// CITYWALK_TEST_REDIS_ADDR, and CITYWALK_TEST_CLICKHOUSE_DSN pointing at scratch instances. No
// ClickHouse instance was reachable in the sandbox this pass was built in (port 8123 closed), so this
// suite is written and compiled but has not been run against a live server; it skips cleanly wherever
// CITYWALK_TEST_CLICKHOUSE_DSN is unset, here and in CI alike.
package mirror_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/event/ingest"
	"github.com/0x0c/citywalk/internal/event/mirror"
	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/event/ratelimit"
	"github.com/0x0c/citywalk/internal/platform/clickhouse"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/internal/platform/redisclient"
	"github.com/0x0c/citywalk/migrations"
)

func testDeps(t *testing.T) (*pgxpool.Pool, *redis.Client, *clickhouse.Client) {
	t.Helper()
	pgDSN := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("CITYWALK_TEST_REDIS_ADDR")
	chDSN := os.Getenv("CITYWALK_TEST_CLICKHOUSE_DSN")
	if pgDSN == "" || redisAddr == "" || chDSN == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN, CITYWALK_TEST_REDIS_ADDR, and CITYWALK_TEST_CLICKHOUSE_DSN must all be set")
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

	conn, err := clickhouse.New(ctx, chDSN)
	if err != nil {
		t.Fatalf("clickhouse.New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(ctx, "TRUNCATE TABLE events_raw"); err != nil {
		t.Fatalf("truncate events_raw: %v", err)
	}

	for _, table := range []string{"event_consumer_offsets", "events_log", "channels"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	return pool, redisClient, &clickhouse.Client{Conn: conn}
}

// TestMirrorWorkerWorkInsertsAcceptedEventsIntoClickHouse proves MirrorWorker.Work actually drains
// events_log into ClickHouse (CW-0010 Unit 6 / CW-0009 Unit 4), not just that it compiles: an event is
// accepted through the ordinary ingest path but never mirrored, then Work runs once, and events_raw
// carries exactly that event.
func TestMirrorWorkerWorkInsertsAcceptedEventsIntoClickHouse(t *testing.T) {
	pool, redisClient, chClient := testDeps(t)
	ctx := context.Background()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	eventID := "01912d2c-0000-7000-8000-000000000001"
	events := []model.Event{
		{ID: eventID, ChannelID: channelID, Kind: model.KindCustom, Name: "screen_view", DeviceTime: now},
	}
	if _, err := ingest.Accept(ctx, ingest.PostgresPublisher{Pool: pool}, limiter, channelID, events, now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	worker := &mirror.MirrorWorker{Pool: pool, Client: chClient}
	job := &river.Job[mirror.MirrorArgs]{Args: mirror.MirrorArgs{}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("Work: %v", err)
	}

	var count uint64
	if err := chClient.Conn.QueryRow(ctx, "SELECT count() FROM events_raw WHERE id = ?", eventID).Scan(&count); err != nil {
		t.Fatalf("query events_raw: %v", err)
	}
	if count != 1 {
		t.Errorf("events_raw count for %s = %d, want 1", eventID, count)
	}
}

// TestMirrorWorkerWorkIsIdempotentOnRerunWithNoNewEvents proves a second firing with nothing new in
// events_log neither errors nor inserts anything further — the offset advance in RunOnce actually
// takes effect between calls.
func TestMirrorWorkerWorkIsIdempotentOnRerunWithNoNewEvents(t *testing.T) {
	pool, redisClient, chClient := testDeps(t)
	ctx := context.Background()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	eventID := "01912d2c-0000-7000-8000-000000000002"
	events := []model.Event{
		{ID: eventID, ChannelID: channelID, Kind: model.KindCustom, Name: "screen_view", DeviceTime: now},
	}
	if _, err := ingest.Accept(ctx, ingest.PostgresPublisher{Pool: pool}, limiter, channelID, events, now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	worker := &mirror.MirrorWorker{Pool: pool, Client: chClient}
	job := &river.Job[mirror.MirrorArgs]{Args: mirror.MirrorArgs{}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("Work (first): %v", err)
	}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("Work (second): %v", err)
	}
	if err := chClient.Conn.Exec(ctx, "OPTIMIZE TABLE events_raw FINAL"); err != nil {
		t.Fatalf("OPTIMIZE TABLE events_raw FINAL: %v", err)
	}

	var count uint64
	if err := chClient.Conn.QueryRow(ctx, "SELECT count() FROM events_raw WHERE id = ?", eventID).Scan(&count); err != nil {
		t.Fatalf("query events_raw: %v", err)
	}
	if count != 1 {
		t.Errorf("events_raw count for %s after two firings = %d, want 1", eventID, count)
	}
}
