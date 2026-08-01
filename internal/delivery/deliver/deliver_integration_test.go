//go:build integration

// Run with: go test -tags=integration ./internal/delivery/deliver/... with
// CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package deliver_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/delivery/deliver"
	"github.com/0x0c/citywalk/internal/membership/batch"
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

	for _, table := range []string{"conversion_attributions", "events_log", "segment_membership", "segments", "channel_ordinals", "channels", "messages"} {
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

func setUpEligibleChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client, now time.Time) string {
	t.Helper()
	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ($1) RETURNING id`,
		map[string]any{"country": "JP"},
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
	seg, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	msg := &model.Message{
		Name: "Konnichiwa", State: model.MessageStateActive,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: seg.ID,
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{Presentation: model.Presentation{Heading: "Hi"}},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}
	return channelID
}

func testConfig() deliver.Config {
	return deliver.Config{
		SizeCeilingBytes: 0, SyncInterval: 15 * time.Minute, SyncJitterFraction: 0.2,
		TagCacheTTL: 30 * time.Second,
	}
}

// TestSyncFirstRequestReturnsThePayload demonstrates a device with no prior tag gets the full
// payload and a fresh ETag.
func TestSyncFirstRequestReturnsThePayload(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := setUpEligibleChannel(t, ctx, pool, redisClient, now)

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", now, testConfig())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Unchanged {
		t.Fatal("Unchanged = true on a first request with no client tag, want false")
	}
	if result.ETag == "" {
		t.Error("ETag is empty")
	}
	if result.Payload == nil || len(result.Payload.Entries) != 1 {
		t.Fatalf("Payload = %+v, want one entry", result.Payload)
	}
}

// TestSyncWithAMatchingTagServesTheFastPathWithNoAssembly demonstrates CW-0006 Unit 2's whole
// point: a repeat synchronization inside the cache's TTL, with a tag the server already cached,
// returns Unchanged without a Payload — the response the 50-millisecond budget is for.
func TestSyncWithAMatchingTagServesTheFastPathWithNoAssembly(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := setUpEligibleChannel(t, ctx, pool, redisClient, now)

	first, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", now, testConfig())
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}

	second, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", first.ETag, now.Add(time.Minute), testConfig())
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if !second.Unchanged {
		t.Error("Unchanged = false on a matching tag, want true")
	}
	if second.Payload != nil {
		t.Error("Payload is non-nil on an unchanged response, want nil")
	}
	if second.ETag != first.ETag {
		t.Errorf("ETag = %q, want it to match the first response's %q", second.ETag, first.ETag)
	}
}

// TestSyncWithAStaleTagReturnsTheNewPayload demonstrates that a client tag from before a campaign
// changed gets the fresh payload and a new tag, not Unchanged.
func TestSyncWithAStaleTagReturnsTheNewPayload(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := setUpEligibleChannel(t, ctx, pool, redisClient, now)

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "some-stale-tag-from-before", now, testConfig())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Unchanged {
		t.Error("Unchanged = true for a stale tag, want false")
	}
	if result.Payload == nil {
		t.Error("Payload is nil, want the fresh payload")
	}
}

// TestSyncAlwaysReturnsANextSyncAt demonstrates CW-0006 Unit 5 (reusing CW-0002's sync package): a
// response — including the fast, unchanged path — always carries a next-synchronization hint.
func TestSyncAlwaysReturnsANextSyncAt(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := setUpEligibleChannel(t, ctx, pool, redisClient, now)
	cfg := testConfig()

	first, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", now, cfg)
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}
	if !first.NextSyncAt.After(now) {
		t.Errorf("NextSyncAt = %v, want after %v", first.NextSyncAt, now)
	}

	second, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", first.ETag, now.Add(time.Minute), cfg)
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if !second.NextSyncAt.After(now.Add(time.Minute)) {
		t.Errorf("NextSyncAt = %v, want after %v even on the unchanged path", second.NextSyncAt, now.Add(time.Minute))
	}
}
