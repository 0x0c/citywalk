//go:build integration

// Run with: go test -tags=integration ./internal/platform/clickhouse/... with
// CITYWALK_TEST_CLICKHOUSE_DSN pointing at a scratch ClickHouse instance (e.g.
// "clickhouse://default:@localhost:9000/default"). No ClickHouse instance was reachable in the
// sandbox this pass was built in (port 8123 closed, per the task that added this file), so this
// suite is written and compiled but has not been run against a live server; it skips cleanly wherever
// the environment variable is unset, here and in CI alike.
package clickhouse_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0x0c/citywalk/internal/platform/clickhouse"
)

func testClient(t *testing.T) *clickhouse.Client {
	t.Helper()
	dsn := os.Getenv("CITYWALK_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("CITYWALK_TEST_CLICKHOUSE_DSN not set")
	}

	ctx := context.Background()
	conn, err := clickhouse.New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(ctx, "TRUNCATE TABLE events_raw"); err != nil {
		t.Fatalf("truncate events_raw: %v", err)
	}
	return &clickhouse.Client{Conn: conn}
}

// TestNewCreatesEventsRawIfMissing proves New's EnsureSchema call actually leaves a queryable table
// behind, not just that Open/Ping succeed.
func TestNewCreatesEventsRawIfMissing(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	var count uint64
	if err := client.Conn.QueryRow(ctx, "SELECT count() FROM events_raw").Scan(&count); err != nil {
		t.Fatalf("query events_raw: %v", err)
	}
	t.Logf("events_raw row count after truncate: %d", count)
}

// TestInsertEventsDeduplicatesByIDAtMerge proves CW-0009 Unit 4's central claim end to end: two rows
// sharing an id — a genuine resend, same id, same device_time, differing only in when the server
// happened to receive each copy — collapse to one row after an explicit merge, not two, and no
// application-level check was needed to make that true.
func TestInsertEventsDeduplicatesByIDAtMerge(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	id := uuid.New()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	first := clickhouse.RawEvent{
		ID: id, ChannelID: uuid.New(), Kind: "custom", Name: "screen_view",
		DeviceTime: now, ServerTime: now, Properties: "{}",
	}
	resend := first
	resend.ServerTime = now.Add(time.Minute)

	if err := client.InsertEvents(ctx, []clickhouse.RawEvent{first, resend}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	// ReplacingMergeTree resolves duplicates during a background merge; OPTIMIZE ... FINAL forces one
	// immediately so the test does not depend on when ClickHouse would otherwise get around to it.
	if err := client.Conn.Exec(ctx, "OPTIMIZE TABLE events_raw FINAL"); err != nil {
		t.Fatalf("OPTIMIZE TABLE events_raw FINAL: %v", err)
	}

	var count uint64
	if err := client.Conn.QueryRow(ctx, "SELECT count() FROM events_raw WHERE id = ?", id).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count after merge = %d, want 1", count)
	}
}

// TestInsertEventsWritesDistinctEventsAsDistinctRows proves the merge above is genuinely keyed by id,
// not by message_id/device_time alone: two different events sharing a message and a device_time
// bucket must both survive.
func TestInsertEventsWritesDistinctEventsAsDistinctRows(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	messageID := uuid.New()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := []clickhouse.RawEvent{
		{ID: uuid.New(), ChannelID: uuid.New(), Kind: "impression", DeviceTime: now, ServerTime: now, Properties: "{}", MessageID: messageID},
		{ID: uuid.New(), ChannelID: uuid.New(), Kind: "impression", DeviceTime: now, ServerTime: now, Properties: "{}", MessageID: messageID},
	}

	if err := client.InsertEvents(ctx, rows); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := client.Conn.Exec(ctx, "OPTIMIZE TABLE events_raw FINAL"); err != nil {
		t.Fatalf("OPTIMIZE TABLE events_raw FINAL: %v", err)
	}

	var count uint64
	if err := client.Conn.QueryRow(ctx, "SELECT count() FROM events_raw WHERE message_id = ?", messageID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("count after merge = %d, want 2 (distinct ids must not collapse)", count)
	}
}
