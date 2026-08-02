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
	"github.com/0x0c/citywalk/internal/event/attribution"
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

	for _, table := range []string{"conversion_attributions", "message_audit_log", "events_log", "segment_membership", "segments", "channel_ordinals", "channels", "messages"} {
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

// TestBuildAssignsAVariantDeterministicallyAcrossMultipleVariants is CW-0008 Units 2 and 3 wired into
// the live delivery path: a message with two variants for the same language selects one of them by
// assignment rather than always the first, and repeated builds for the same channel return the same
// variant every time — CW-0008's whole premise, computed fresh rather than stored.
func TestBuildAssignsAVariantDeterministicallyAcrossMultipleVariants(t *testing.T) {
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
		Name: "A/B test", State: model.MessageStateActive,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: seg.ID,
		Variants: []model.Variant{
			{Weight: 50, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor}, Content: model.DialogContent{Presentation: model.Presentation{Heading: "A"}}},
			{Weight: 50, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor}, Content: model.DialogContent{Presentation: model.Presentation{Heading: "B"}}},
		},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	first, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (first): %v", err)
	}
	if len(first.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(first.Entries))
	}
	assignedVariant := first.Entries[0].VariantID

	found := false
	for _, v := range msg.Variants {
		if v.ID == assignedVariant {
			found = true
		}
	}
	if !found {
		t.Fatalf("assigned variant %s is not one of the message's own variants", assignedVariant)
	}

	for i := 0; i < 5; i++ {
		again, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, 0, 15*time.Minute, 0.2)
		if err != nil {
			t.Fatalf("Build (repeat %d): %v", i, err)
		}
		if len(again.Entries) != 1 || again.Entries[0].VariantID != assignedVariant {
			t.Fatalf("Build (repeat %d) assigned %v, want the same variant %s every time", i, again.Entries, assignedVariant)
		}
	}
}

// TestBuildExcludesAMessageWhenTheChannelLandsInItsHoldout is CW-0008 Unit 4 wired into the live
// delivery path: a message whose entire bucket space is reserved for its holdout never appears in
// any channel's payload — eligible in every respect, but excluded exactly like a channel that never
// qualified.
func TestBuildExcludesAMessageWhenTheChannelLandsInItsHoldout(t *testing.T) {
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
		Name: "Fully held out", State: model.MessageStateActive,
		Window:          model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef:     seg.ID,
		HoldoutFraction: 1.0,
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
		t.Errorf("len(Entries) = %d, want 0 (message is 100%% held out)", len(p.Entries))
	}
}

// TestBuildEmitsAHoldoutQualifiedEventWhenTheChannelLandsInItsHoldout is CW-0008 Unit 5 wired into
// the live delivery path: a channel excluded by its own message's holdout is not just silently
// dropped from the payload — it leaves a KindHoldoutQualified row behind, the denominator the
// counterfactual comparison a holdout exists for needs.
func TestBuildEmitsAHoldoutQualifiedEventWhenTheChannelLandsInItsHoldout(t *testing.T) {
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
		Name: "Fully held out", State: model.MessageStateActive,
		Window:          model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef:     seg.ID,
		HoldoutFraction: 1.0,
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
		t.Errorf("len(Entries) = %d, want 0 (message is 100%% held out)", len(p.Entries))
	}

	var count int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM events_log WHERE channel_id = $1 AND kind = 'holdout_qualified' AND message_id = $2`,
		channelID, msg.ID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query events_log: %v", err)
	}
	if count != 1 {
		t.Errorf("holdout_qualified event count = %d, want 1", count)
	}
}

// TestHoldoutQualifiedEventFromBuildIsCountedByAttribution carries CW-0008 Unit 5's counterfactual all
// the way to CW-0009's reporting side: the holdout_qualified row Build leaves behind for an excluded
// channel is not just a row in events_log — attribution.Run picks it up as an exposure exactly like an
// impression, and a later conversion from that same channel attributes to the holdout
// (attribution.HoldoutVariantID), which is what gives the counterfactual comparison a holdout exists
// for its denominator.
func TestHoldoutQualifiedEventFromBuildIsCountedByAttribution(t *testing.T) {
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
		Name: "Fully held out", State: model.MessageStateActive,
		Window:          model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef:     seg.ID,
		HoldoutFraction: 1.0,
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

	if _, err := payload.Build(ctx, pool, redisClient, channelID, "en", now, 0, 15*time.Minute, 0.2); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// A conversion 10 minutes after the holdout qualification Build just recorded, exactly as a
	// device-submitted custom event would arrive.
	if _, err := pool.Exec(ctx,
		`INSERT INTO events_log (id, channel_id, kind, name, device_time) VALUES (gen_random_uuid(), $1, 'custom', 'purchase', $2)`,
		channelID, now.Add(10*time.Minute),
	); err != nil {
		t.Fatalf("insert conversion event: %v", err)
	}

	if err := attribution.Run(ctx, pool, msg.ID, "purchase", time.Hour); err != nil {
		t.Fatalf("attribution.Run: %v", err)
	}

	counts, err := attribution.Counts(ctx, pool, msg.ID)
	if err != nil {
		t.Fatalf("attribution.Counts: %v", err)
	}
	if counts[attribution.HoldoutVariantID] != 1 {
		t.Errorf("counts[HoldoutVariantID] = %d, want 1 (the real holdout_qualified event Build recorded)", counts[attribution.HoldoutVariantID])
	}
}

// TestBuildExcludesAMessageWithNoVariantSupportingTheChannelsDeclaredSchemaMajor is CW-0003 Unit 4's
// compatibility check wired into the live delivery path: "each device receives the highest version
// its SDK declares support for" means a device that declared a schema major no variant on an
// otherwise-eligible message carries gets no entry for it at all, the same as a channel that never
// qualified.
func TestBuildExcludesAMessageWithNoVariantSupportingTheChannelsDeclaredSchemaMajor(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// A channel that declared support for a schema major no persisted variant carries (every variant
	// in this pass is model.CurrentMajor, since save-time validation admits nothing else).
	channelID := insertChannelWithSchemaMajor(t, ctx, pool, map[string]any{"country": "JP"}, model.CurrentMajor+1)

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
		Name: "Incompatible schema major", State: model.MessageStateActive,
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
		t.Errorf("len(Entries) = %d, want 0 (no variant supports this channel's declared schema major)", len(p.Entries))
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

func insertChannelWithSchemaMajor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attrs map[string]any, supportedSchemaMajor int) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO channels (attributes, supported_schema_major) VALUES ($1, $2) RETURNING id`,
		attrs, supportedSchemaMajor,
	).Scan(&id); err != nil {
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
