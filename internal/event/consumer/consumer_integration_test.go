//go:build integration

// Run with: go test -tags=integration ./internal/event/consumer/... with CITYWALK_TEST_POSTGRES_DSN
// pointing at a scratch PostgreSQL database.
package consumer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

// TestApplyBatchIsIdempotentAcrossSeparateTransactions is CW-0009 Unit 3's remaining requirement,
// proved directly against the function both consumers share: a batch reprocessed after the position
// that tracked it failed to advance must not double count. That is RunOnceFromLog's actual failure
// mode — the log's consumer-group offset commit is a system separate from the Postgres transaction
// holding the rollup writes, so a crash between the two replays the batch on the next run — reproduced
// here by calling applyBatch a second time, in a second transaction, with the identical events. A test
// that only calls RunOnce cannot exercise this: RunOnce's own offset advance and rollup writes share
// one transaction, so RunOnce never observes a retry by construction, and proving the guard actually
// holds needs to bypass that and call the shared rollup-writing logic itself twice.
func TestApplyBatchIsIdempotentAcrossSeparateTransactions(t *testing.T) {
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
	for _, table := range []string{
		"rollup_applied_events", "suppression_rollup", "reach_sketch", "campaign_rollup",
		"targeting_rollup", "event_consumer_offsets", "events_log", "variants", "messages", "channels",
	} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	var messageID, variantID string
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := pool.QueryRow(ctx, `
		INSERT INTO messages (name, state, priority, window_start, window_end)
		VALUES ('test', 'active', 0, $1, $2) RETURNING id
	`, now.Add(-time.Hour), now.Add(time.Hour)).Scan(&messageID); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO variants (message_id, weight, language, content) VALUES ($1, 100, 'en', '{}') RETURNING id
	`, messageID).Scan(&variantID); err != nil {
		t.Fatalf("insert variant: %v", err)
	}

	// One event that touches the targeting rollup, the campaign rollup, and the reach sketch
	// (an impression carrying a message id), and one that touches only the targeting rollup (a
	// custom event with no message id) — between them, every rollup applyBatch writes to.
	events := []storedEvent{
		{
			ID: "01912d2c-1000-7000-8000-000000000001", ChannelID: channelID,
			Kind: model.KindImpression, MessageID: messageID, VariantID: variantID,
			DeviceTime: now, ServerTime: now,
		},
		{
			ID: "01912d2c-1000-7000-8000-000000000002", ChannelID: channelID,
			Kind: model.KindCustom, Name: "screen_view", DeviceTime: now, ServerTime: now,
		},
	}

	applyInOwnTransaction(t, ctx, pool, events)
	// The replay: the same events, applied again in a fresh transaction, exactly as RunOnceFromLog
	// would if its log offset commit never landed after this rollup transaction already had.
	applyInOwnTransaction(t, ctx, pool, events)

	var targetingCount int64
	if err := pool.QueryRow(ctx,
		`SELECT count FROM targeting_rollup WHERE channel_id = $1 AND event_name = 'screen_view'`, channelID,
	).Scan(&targetingCount); err != nil {
		t.Fatalf("query targeting_rollup: %v", err)
	}
	if targetingCount != 1 {
		t.Errorf("targeting_rollup screen_view count = %d, want 1 (a replayed batch must not double count)", targetingCount)
	}

	var campaignCount int64
	if err := pool.QueryRow(ctx,
		`SELECT count FROM campaign_rollup WHERE message_id = $1 AND variant_id = $2 AND kind = 'impression'`,
		messageID, variantID,
	).Scan(&campaignCount); err != nil {
		t.Fatalf("query campaign_rollup: %v", err)
	}
	if campaignCount != 1 {
		t.Errorf("campaign_rollup impression count = %d, want 1 (a replayed batch must not double count)", campaignCount)
	}

	var appliedRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM rollup_applied_events`).Scan(&appliedRows); err != nil {
		t.Fatalf("count rollup_applied_events: %v", err)
	}
	if appliedRows != len(events) {
		t.Errorf("rollup_applied_events has %d rows, want %d (one per distinct event, not one per applyBatch call)", appliedRows, len(events))
	}
}

func applyInOwnTransaction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, events []storedEvent) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applyBatch(ctx, tx, events); err != nil {
		t.Fatalf("applyBatch: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
