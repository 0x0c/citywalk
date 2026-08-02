//go:build integration

// Run with: go test -tags=integration ./internal/event/clockoffset/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch Postgres instance.
package clockoffset_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/clockoffset"
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
	for _, table := range []string{"events_log", "channels"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	return pool
}

func insertChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	return id
}

func insertEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channelID string, deviceTime, serverTime time.Time) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO events_log (id, channel_id, kind, name, device_time, server_time)
		VALUES (gen_random_uuid(), $1, 'custom', 'screen_view', $2, $3)
	`, channelID, deviceTime, serverTime)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

// TestEstimateReadsAChannelsConsistentSkewFromItsLoggedHistory is Unit 1's offset estimate end to
// end: a channel whose every event was received 90 seconds after its device_time claims must have
// that skew recovered from events_log, not just from an in-memory synthetic history.
func TestEstimateReadsAChannelsConsistentSkewFromItsLoggedHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)

	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		deviceTime := base.Add(time.Duration(i) * time.Minute)
		serverTime := deviceTime.Add(-90 * time.Second) // received 90s "before" its own device time claims
		insertEvent(t, ctx, pool, channelID, deviceTime, serverTime)
	}

	offset, err := clockoffset.Estimate(ctx, pool, channelID, clockoffset.DefaultSampleSize)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if offset != 90*time.Second {
		t.Errorf("Estimate() = %v, want 90s", offset)
	}
}

// TestEstimateOfAChannelWithNoEventsIsZero confirms the no-history default: a channel Estimate has
// never seen an event for reports no offset, so Correct is a no-op against it.
func TestEstimateOfAChannelWithNoEventsIsZero(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)

	offset, err := clockoffset.Estimate(ctx, pool, channelID, clockoffset.DefaultSampleSize)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if offset != 0 {
		t.Errorf("Estimate() for a channel with no events = %v, want 0", offset)
	}
}

// TestEstimateIsPerChannel confirms the estimate does not leak across channels: two channels with
// opposite skews must each report their own, not an average of both.
func TestEstimateIsPerChannel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	fastChannel := insertChannel(t, ctx, pool)
	slowChannel := insertChannel(t, ctx, pool)

	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		deviceTime := base.Add(time.Duration(i) * time.Minute)
		insertEvent(t, ctx, pool, fastChannel, deviceTime, deviceTime.Add(-30*time.Second))
		insertEvent(t, ctx, pool, slowChannel, deviceTime, deviceTime.Add(30*time.Second))
	}

	fastOffset, err := clockoffset.Estimate(ctx, pool, fastChannel, clockoffset.DefaultSampleSize)
	if err != nil {
		t.Fatalf("Estimate(fastChannel): %v", err)
	}
	if fastOffset != 30*time.Second {
		t.Errorf("Estimate(fastChannel) = %v, want 30s", fastOffset)
	}

	slowOffset, err := clockoffset.Estimate(ctx, pool, slowChannel, clockoffset.DefaultSampleSize)
	if err != nil {
		t.Fatalf("Estimate(slowChannel): %v", err)
	}
	if slowOffset != -30*time.Second {
		t.Errorf("Estimate(slowChannel) = %v, want -30s", slowOffset)
	}
}
