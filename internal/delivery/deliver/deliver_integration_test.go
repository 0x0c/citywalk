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
	"github.com/0x0c/citywalk/internal/delivery/changelog"
	"github.com/0x0c/citywalk/internal/delivery/cursor"
	"github.com/0x0c/citywalk/internal/delivery/deliver"
	"github.com/0x0c/citywalk/internal/delivery/payload"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/membership/ordinal"
	"github.com/0x0c/citywalk/internal/membership/reverse"
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

	for _, table := range []string{"conversion_attributions", "message_audit_log", "delivery_change_log", "events_log", "segment_membership", "segments", "channel_ordinals", "channels", "messages"} {
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

func deltaTestConfig() deliver.Config {
	cfg := testConfig()
	cfg.DeltaModeEnabled = true
	return cfg
}

// setUpChannelAndSegment is setUpEligibleChannel's split-apart form for CW-0006 Unit 3's tests: they
// need to insert a second message into the same segment mid-test, which setUpEligibleChannel's own
// all-in-one shape does not expose a seam for.
func setUpChannelAndSegment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client) (channelID, segmentID string) {
	t.Helper()
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
	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}
	return channelID, seg.ID
}

// insertActiveMessageAndRecordUpsert inserts msg directly active in segmentID (bypassing
// AdminService's forced-draft CreateMessage, the same shortcut every other delivery-path test in
// this repository already takes) and reproduces both of CW-0006's own write hooks a real
// AdminService.UpdateMessageState call would have run — Unit 3's change log row and Unit 4's bundle
// cache invalidation — since internal/platform/connectserver/admin.go now populates a bundle cache
// (CW-0006 Unit 4) that these tests must keep consistent with or a stale cached bundle, not the
// change log, would answer the second Sync call. admin.go's own wiring is integration-tested
// separately in that package; this helper only needs both hooks' effects, not a second proof that
// the wiring exists.
func insertActiveMessageAndRecordUpsert(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client, segmentID, heading string, now time.Time,
) string {
	t.Helper()
	msg := &model.Message{
		Name: heading, State: model.MessageStateActive,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: segmentID,
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{Presentation: model.Presentation{Heading: heading}},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage(%s): %v", heading, err)
	}
	if err := changelog.Record(ctx, pool, msg.ID, changelog.KindUpsert, now); err != nil {
		t.Fatalf("changelog.Record(%s): %v", heading, err)
	}
	if err := payload.InvalidateCampaign(ctx, pool, redisClient, msg.ID); err != nil {
		t.Fatalf("InvalidateCampaign(%s): %v", heading, err)
	}
	return msg.ID
}

// TestSyncDeltaModeGivesAFullPayloadAndAFreshCursorOnFirstRequest demonstrates that a device's first
// synchronization under delta mode — no cursor to honor yet — behaves exactly like a full sync always
// has, plus a cursor for the device to use next time.
func TestSyncDeltaModeGivesAFullPayloadAndAFreshCursorOnFirstRequest(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID, segmentID := setUpChannelAndSegment(t, ctx, pool, redisClient)
	insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "Hi", now)

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "", now, deltaTestConfig())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.IsDelta {
		t.Error("IsDelta = true on a first request with no cursor, want false")
	}
	if result.Payload == nil || len(result.Payload.Entries) != 1 {
		t.Fatalf("Payload = %+v, want one entry", result.Payload)
	}
	if result.Cursor == "" {
		t.Error("Cursor is empty, want a fresh cursor since delta mode is enabled")
	}
}

// TestSyncDeltaModeReturnsOnlyTheChangedEntry is CW-0006 Unit 3's core promise: a device with a
// recent, valid cursor gets only the message that changed since it, not the one that didn't.
func TestSyncDeltaModeReturnsOnlyTheChangedEntry(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cfg := deltaTestConfig()

	channelID, segmentID := setUpChannelAndSegment(t, ctx, pool, redisClient)
	insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "Unchanged", now)

	first, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "", now, cfg)
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}
	if len(first.Payload.Entries) != 1 {
		t.Fatalf("first Payload.Entries = %+v, want 1 entry", first.Payload.Entries)
	}

	later := now.Add(time.Minute)
	newMessageID := insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "New", later)

	// A deliberately stale ETag, not first.ETag: the per-channel tag cache (CW-0006 Unit 2) has a
	// real 30-second TTL measured in wall-clock time, which this test's simulated business clock
	// does not advance, so first.ETag would still be a cache hit and short-circuit before Unit 3's
	// delta path ever runs — the same reason TestSyncWithAStaleTagReturnsTheNewPayload above uses a
	// synthetic tag rather than reusing a prior real one.
	second, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "stale-etag-from-before", first.Cursor, later, cfg)
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if !second.IsDelta {
		t.Fatal("IsDelta = false, want true for a cursor CW-0006 Unit 3 can still honor")
	}
	if len(second.Payload.Entries) != 1 || second.Payload.Entries[0].MessageID != newMessageID {
		t.Errorf("second Payload.Entries = %+v, want exactly the new message %s", second.Payload.Entries, newMessageID)
	}
	if len(second.Tombstones) != 0 {
		t.Errorf("Tombstones = %v, want none — nothing was removed", second.Tombstones)
	}
	if second.Cursor == "" || second.Cursor == first.Cursor {
		t.Error("Cursor was not refreshed on a delta response")
	}
}

// TestSyncDeltaModeReturnsATombstoneForARemovedMessage demonstrates the other half of CW-0006 Unit
// 3's promise: a message a channel lost eligibility for (here, the kill switch) is announced as a
// tombstone, not silently omitted.
func TestSyncDeltaModeReturnsATombstoneForARemovedMessage(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cfg := deltaTestConfig()

	channelID, segmentID := setUpChannelAndSegment(t, ctx, pool, redisClient)
	messageID := insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "Will be paused", now)

	first, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "", now, cfg)
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}
	if len(first.Payload.Entries) != 1 {
		t.Fatalf("first Payload.Entries = %+v, want 1 entry", first.Payload.Entries)
	}

	later := now.Add(time.Minute)
	if err := store.UpdateState(ctx, pool, messageID, model.MessageStatePaused, "test-actor", later); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if err := changelog.Record(ctx, pool, messageID, changelog.KindTombstone, later); err != nil {
		t.Fatalf("changelog.Record: %v", err)
	}
	// CW-0006 Unit 4's own hook, alongside Unit 3's — see insertActiveMessageAndRecordUpsert's own
	// comment on why both must run together in these tests.
	if err := payload.InvalidateCampaign(ctx, pool, redisClient, messageID); err != nil {
		t.Fatalf("InvalidateCampaign: %v", err)
	}

	// A deliberately stale ETag, not first.ETag — see TestSyncDeltaModeReturnsOnlyTheChangedEntry's
	// own comment on why reusing the real prior tag would hit the wall-clock TTL cache instead.
	second, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "stale-etag-from-before", first.Cursor, later, cfg)
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if !second.IsDelta {
		t.Fatal("IsDelta = false, want true")
	}
	if len(second.Payload.Entries) != 0 {
		t.Errorf("second Payload.Entries = %+v, want none", second.Payload.Entries)
	}
	if len(second.Tombstones) != 1 || second.Tombstones[0] != messageID {
		t.Errorf("Tombstones = %v, want exactly [%s]", second.Tombstones, messageID)
	}
}

// TestSyncDeltaModeFallsBackToFullOnAnUnrecognizedCursor demonstrates CW-0006 Unit 3's stated
// contract: a cursor the server cannot make sense of produces a full payload and a fresh cursor,
// never an error.
func TestSyncDeltaModeFallsBackToFullOnAnUnrecognizedCursor(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID, segmentID := setUpChannelAndSegment(t, ctx, pool, redisClient)
	insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "Hi", now)

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "garbage-not-a-real-cursor", now, deltaTestConfig())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.IsDelta {
		t.Error("IsDelta = true for a garbage cursor, want false (fall back to full)")
	}
	if result.Payload == nil || len(result.Payload.Entries) != 1 {
		t.Fatalf("Payload = %+v, want the full one-entry payload", result.Payload)
	}
	if result.Cursor == "" {
		t.Error("Cursor is empty, want a fresh one even on the fallback path")
	}
}

// TestSyncDeltaModeFallsBackToFullWhenTheCursorPredatesTheRetentionWindow demonstrates the change
// log's bound doing its job: a cursor older than changelog.Retention cannot be trusted (rows it would
// need may have been pruned), so the server falls back to full rather than risk a silently incomplete
// delta.
func TestSyncDeltaModeFallsBackToFullWhenTheCursorPredatesTheRetentionWindow(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID, segmentID := setUpChannelAndSegment(t, ctx, pool, redisClient)
	insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "Hi", now)

	membershipBM, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		t.Fatalf("reverse.Get: %v", err)
	}
	membershipHash, err := reverse.Hash(membershipBM)
	if err != nil {
		t.Fatalf("reverse.Hash: %v", err)
	}
	// Otherwise honorable in every other respect (a real membership hash, a seq that exists) — only
	// IssuedAt is too old, isolating the retention check from every other fallback reason.
	staleCursor := cursor.Encode(cursor.Cursor{
		Seq: 0, MembershipHash: membershipHash, IssuedAt: now.Add(-changelog.Retention - time.Hour),
	})

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", staleCursor, now, deltaTestConfig())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.IsDelta {
		t.Error("IsDelta = true for a cursor older than the retention window, want false")
	}
	if result.Payload == nil || len(result.Payload.Entries) != 1 {
		t.Fatalf("Payload = %+v, want the full one-entry payload", result.Payload)
	}
}

// TestSyncDeltaModeDisabledNeverProducesADeltaOrACursor demonstrates that delta mode's default
// (disabled) preserves exactly today's behavior, whether or not a device sends a cursor.
func TestSyncDeltaModeDisabledNeverProducesADeltaOrACursor(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID, segmentID := setUpChannelAndSegment(t, ctx, pool, redisClient)
	insertActiveMessageAndRecordUpsert(t, ctx, pool, redisClient, segmentID, "Hi", now)

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "some-cursor-that-is-ignored", now, testConfig())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.IsDelta {
		t.Error("IsDelta = true with delta mode disabled, want false")
	}
	if result.Cursor != "" {
		t.Errorf("Cursor = %q, want empty with delta mode disabled", result.Cursor)
	}
	if result.Payload == nil || len(result.Payload.Entries) != 1 {
		t.Fatalf("Payload = %+v, want the full one-entry payload", result.Payload)
	}
}

// TestSyncFirstRequestReturnsThePayload demonstrates a device with no prior tag gets the full
// payload and a fresh ETag.
func TestSyncFirstRequestReturnsThePayload(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	channelID := setUpEligibleChannel(t, ctx, pool, redisClient, now)

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "", now, testConfig())
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

	first, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "", now, testConfig())
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}

	second, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", first.ETag, "", now.Add(time.Minute), testConfig())
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

	result, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "some-stale-tag-from-before", "", now, testConfig())
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

	first, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", "", "", now, cfg)
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}
	if !first.NextSyncAt.After(now) {
		t.Errorf("NextSyncAt = %v, want after %v", first.NextSyncAt, now)
	}

	second, err := deliver.Sync(ctx, pool, redisClient, channelID, "en", first.ETag, "", now.Add(time.Minute), cfg)
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if !second.NextSyncAt.After(now.Add(time.Minute)) {
		t.Errorf("NextSyncAt = %v, want after %v even on the unchanged path", second.NextSyncAt, now.Add(time.Minute))
	}
}
