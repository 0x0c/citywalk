//go:build integration

// Run with: go test -tags=integration ./internal/governance/budget/... with
// CITYWALK_TEST_REDIS_ADDR pointing at a scratch Redis instance.
package budget_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/governance/budget"
	"github.com/0x0c/citywalk/internal/platform/redisclient"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("CITYWALK_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("CITYWALK_TEST_REDIS_ADDR not set")
	}
	ctx := context.Background()
	client, err := redisclient.New(ctx, addr)
	if err != nil {
		t.Fatalf("redisclient.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}
	return client
}

func TestCheckAndIncrementAllowsUpToTheCapThenDenies(t *testing.T) {
	redisClient := testRedis(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	counter := budget.Counter{Redis: redisClient, Window: 24 * time.Hour}

	for i := 0; i < 2; i++ {
		allowed, err := counter.CheckAndIncrement(ctx, "channel-1", 2, now)
		if err != nil {
			t.Fatalf("CheckAndIncrement: %v", err)
		}
		if !allowed {
			t.Errorf("CheckAndIncrement() call %d = false, want true (within cap of 2)", i)
		}
	}

	allowed, err := counter.CheckAndIncrement(ctx, "channel-1", 2, now)
	if err != nil {
		t.Fatalf("CheckAndIncrement: %v", err)
	}
	if allowed {
		t.Error("CheckAndIncrement() on the 3rd call = true, want false (cap of 2 already spent)")
	}
}

func TestCheckAndIncrementIsPerIdentity(t *testing.T) {
	redisClient := testRedis(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	counter := budget.Counter{Redis: redisClient, Window: 24 * time.Hour}

	if _, err := counter.CheckAndIncrement(ctx, "channel-a", 1, now); err != nil {
		t.Fatalf("CheckAndIncrement: %v", err)
	}
	allowed, err := counter.CheckAndIncrement(ctx, "channel-b", 1, now)
	if err != nil {
		t.Fatalf("CheckAndIncrement: %v", err)
	}
	if !allowed {
		t.Error("CheckAndIncrement() for a different identity = false, want true (separate counters)")
	}
}

// TestEstimateDecaysAcrossAWindowBoundary is the sliding-window property CW-0007 Unit 4 exists for:
// a count from the previous window should count for less as the current window progresses, rather
// than either vanishing instantly at the boundary (a fixed window's burst problem) or persisting at
// full weight forever.
func TestEstimateDecaysAcrossAWindowBoundary(t *testing.T) {
	redisClient := testRedis(t)
	ctx := context.Background()
	counter := budget.Counter{Redis: redisClient, Window: time.Hour}

	windowStart := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if err := counter.RecordImpression(ctx, "channel-1", windowStart); err != nil {
			t.Fatalf("RecordImpression: %v", err)
		}
	}

	earlyInNextWindow := windowStart.Add(time.Hour + time.Minute)
	remainingEarly, err := counter.Remaining(ctx, "channel-1", 4, earlyInNextWindow)
	if err != nil {
		t.Fatalf("Remaining (early): %v", err)
	}

	// Reset and repeat, checking late in the next window instead — the previous bucket's weight has
	// decayed further, so more of the cap should read as available.
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := counter.RecordImpression(ctx, "channel-1", windowStart); err != nil {
			t.Fatalf("RecordImpression: %v", err)
		}
	}
	lateInNextWindow := windowStart.Add(2*time.Hour - time.Minute)
	remainingLate, err := counter.Remaining(ctx, "channel-1", 4, lateInNextWindow)
	if err != nil {
		t.Fatalf("Remaining (late): %v", err)
	}

	if remainingLate <= remainingEarly {
		t.Errorf("remaining late (%d) must exceed remaining early (%d): the previous bucket's weight must decay across the window", remainingLate, remainingEarly)
	}
}

func TestRemainingDoesNotRecordAnImpression(t *testing.T) {
	redisClient := testRedis(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	counter := budget.Counter{Redis: redisClient, Window: 24 * time.Hour}

	for i := 0; i < 5; i++ {
		if _, err := counter.Remaining(ctx, "channel-1", 2, now); err != nil {
			t.Fatalf("Remaining: %v", err)
		}
	}
	remaining, err := counter.Remaining(ctx, "channel-1", 2, now)
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 2 {
		t.Errorf("Remaining() after 5 read-only calls = %d, want 2 (unchanged from the cap)", remaining)
	}
}
