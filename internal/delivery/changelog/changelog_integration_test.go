//go:build integration

// Run with: go test -tags=integration ./internal/delivery/changelog/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package changelog_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/delivery/changelog"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	for _, table := range []string{"message_audit_log", "delivery_change_log", "messages"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	return pool
}

func insertMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) string {
	t.Helper()
	msg := &model.Message{
		Name: "Test", State: model.MessageStateDraft,
		Window: model.Window{Start: now, End: now.Add(time.Hour)},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	return msg.ID
}

// TestRecordAssignsIncreasingSeq demonstrates the change log's own cursor value is monotonically
// increasing across rows, the property CW-0006 Unit 3's cursor comparison depends on.
func TestRecordAssignsIncreasingSeq(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	messageID := insertMessage(t, ctx, pool, now)

	if err := changelog.Record(ctx, pool, messageID, changelog.KindUpsert, now); err != nil {
		t.Fatalf("Record (1): %v", err)
	}
	seqAfterFirst, err := changelog.CurrentSeq(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentSeq: %v", err)
	}

	if err := changelog.Record(ctx, pool, messageID, changelog.KindTombstone, now.Add(time.Minute)); err != nil {
		t.Fatalf("Record (2): %v", err)
	}
	seqAfterSecond, err := changelog.CurrentSeq(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentSeq: %v", err)
	}

	if seqAfterSecond <= seqAfterFirst {
		t.Errorf("seq after second record = %d, want greater than the first's %d", seqAfterSecond, seqAfterFirst)
	}
}

// TestSinceReturnsOnlyRowsPastTheGivenSeq demonstrates the query CW-0006 Unit 3's delta computation
// relies on: everything after a cursor, nothing at or before it.
func TestSinceReturnsOnlyRowsPastTheGivenSeq(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	older := insertMessage(t, ctx, pool, now)
	newer := insertMessage(t, ctx, pool, now)

	if err := changelog.Record(ctx, pool, older, changelog.KindUpsert, now); err != nil {
		t.Fatalf("Record (older): %v", err)
	}
	cutoff, err := changelog.CurrentSeq(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentSeq: %v", err)
	}
	if err := changelog.Record(ctx, pool, newer, changelog.KindUpsert, now.Add(time.Minute)); err != nil {
		t.Fatalf("Record (newer): %v", err)
	}

	changes, err := changelog.Since(ctx, pool, cutoff)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1", len(changes))
	}
	if changes[0].MessageID != newer {
		t.Errorf("changes[0].MessageID = %s, want %s (only the row after cutoff)", changes[0].MessageID, newer)
	}
}

// TestRecordPrunesRowsOlderThanRetention demonstrates CW-0006 Unit 3's bound: the change log does
// not grow forever, and a row past Retention is gone the next time anything is recorded.
func TestRecordPrunesRowsOlderThanRetention(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	stale := insertMessage(t, ctx, pool, now)
	fresh := insertMessage(t, ctx, pool, now)

	staleTime := now.Add(-changelog.Retention - time.Hour)
	if err := changelog.Record(ctx, pool, stale, changelog.KindUpsert, staleTime); err != nil {
		t.Fatalf("Record (stale): %v", err)
	}

	// A later Record call, well within retention, opportunistically prunes the stale row (this pass
	// adds no background scheduler — see changelog.Record's own doc comment).
	if err := changelog.Record(ctx, pool, fresh, changelog.KindUpsert, now); err != nil {
		t.Fatalf("Record (fresh): %v", err)
	}

	changes, err := changelog.Since(ctx, pool, 0)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	for _, c := range changes {
		if c.MessageID == stale {
			t.Errorf("Since(0) still returns the stale row for message %s, want it pruned", stale)
		}
	}
	if len(changes) != 1 || changes[0].MessageID != fresh {
		t.Errorf("changes = %+v, want exactly the fresh row for message %s", changes, fresh)
	}
}
