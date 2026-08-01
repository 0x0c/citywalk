//go:build integration

// Run with: go test -tags=integration ./internal/delivery/payload/... with
// CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package payload_test

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
	"github.com/0x0c/citywalk/internal/delivery/payload"
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

	for _, table := range []string{"segment_membership", "segments", "channel_ordinals", "channels", "messages"} {
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

// TestBuildAssemblesAnEligiblePayloadWithNoAudienceData is the end-to-end proof CW-0002 Units 1-3
// exist for: a channel's payload is built from real audience resolution (CW-0004's predicate engine
// through CW-0005's membership index), carries the content a device needs, and carries nothing about
// why the channel qualified.
func TestBuildAssemblesAnEligiblePayloadWithNoAudienceData(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "JP"})

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
		Name: "Welcome to Japan", State: model.MessageStateActive, Priority: 5,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: seg.ID,
		ControlPolicy: model.ControlPolicy{
			PerMessageCap: 3, MinIntervalBetween: time.Hour,
		},
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{Presentation: model.Presentation{Heading: "Konnichiwa"}},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	p, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(p.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(p.Entries))
	}
	entry := p.Entries[0]
	if entry.MessageID != msg.ID {
		t.Errorf("MessageID = %q, want %q", entry.MessageID, msg.ID)
	}
	if !entry.ExpiresAt.Equal(msg.Window.End) {
		t.Errorf("ExpiresAt = %v, want %v", entry.ExpiresAt, msg.Window.End)
	}
	if p.NextSyncAt.Before(now.Add(15 * time.Minute)) {
		t.Errorf("NextSyncAt = %v, want at least 15 minutes after now", p.NextSyncAt)
	}
	if len(entry.Content) == 0 {
		t.Error("Content is empty, want the variant's encoded content")
	}
}

// TestBuildExcludesAnIneligibleChannel demonstrates the boundary rule end to end: a channel outside
// the segment gets an empty payload, not the message.
func TestBuildExcludesAnIneligibleChannel(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "US"})

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
		Name: "Welcome to Japan", State: model.MessageStateActive,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: seg.ID,
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	p, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0 (channel is not in the segment)", len(p.Entries))
	}
}

// TestBuildTruncatesInPriorityOrder demonstrates CW-0002 Unit 2's size ceiling: when the eligible
// set exceeds it, the lower-priority entry is dropped, not the higher-priority one.
func TestBuildTruncatesInPriorityOrder(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "JP"})

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	seg, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	highPriority := newMessage(t, ctx, pool, "High", 10, seg.ID, now, "A long heading that takes up a fair amount of encoded space")
	lowPriority := newMessage(t, ctx, pool, "Low", 1, seg.ID, now, "Another long heading that takes up a fair amount of space too")

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	// Fetch the untruncated payload first to learn one entry's real encoded size, then set the
	// ceiling to fit exactly one.
	full, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (untruncated): %v", err)
	}
	if len(full.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 before truncation", len(full.Entries))
	}
	ceiling := len(full.Entries[0].Content)

	truncated, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, ceiling, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (truncated): %v", err)
	}
	if len(truncated.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1 after truncation", len(truncated.Entries))
	}
	if truncated.Entries[0].MessageID != highPriority {
		t.Errorf("surviving entry = %s, want the high-priority message %s (low-priority %s should be dropped)",
			truncated.Entries[0].MessageID, highPriority, lowPriority)
	}
}

func insertChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attrs map[string]any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ($1) RETURNING id`, attrs).Scan(&id); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := ordinal.Allocate(ctx, pool, id); err != nil {
		t.Fatalf("allocate ordinal: %v", err)
	}
	return id
}

func newMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, priority int, audienceRef string, now time.Time, heading string) string {
	t.Helper()
	msg := &model.Message{
		Name: name, State: model.MessageStateActive, Priority: priority,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: audienceRef,
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{Presentation: model.Presentation{Heading: heading}},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage(%s): %v", name, err)
	}
	return msg.ID
}
