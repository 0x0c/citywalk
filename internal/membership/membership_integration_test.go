//go:build integration

// Run with: go test -tags=integration ./internal/membership/... with CITYWALK_TEST_POSTGRES_DSN and
// CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package membership_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/membership/forward"
	"github.com/0x0c/citywalk/internal/membership/incremental"
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

	for _, table := range []string{"conversion_attributions", "events_log", "segment_membership", "segments", "channel_ordinals", "channels"} {
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

func insertChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attrs map[string]any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ($1) RETURNING id`, attrs).Scan(&id); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := ordinal.Allocate(ctx, pool, id); err != nil {
		t.Fatalf("allocate ordinal for channel %s: %v", id, err)
	}
	return id
}

// TestBatchRecomputeBuildsForwardAndReverseIndexes is the end-to-end proof of CW-0005 Units 1-4: a
// full recomputation over a real channel population produces a forward index the authoring path can
// count, and a reverse index the delivery path can look up per channel, and the two agree with each
// other.
func TestBatchRecomputeBuildsForwardAndReverseIndexes(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()

	jpChannel := insertChannel(t, ctx, pool, map[string]any{"country": "JP", "total_distance_km": 150.0})
	usChannel := insertChannel(t, ctx, pool, map[string]any{"country": "US", "total_distance_km": 5.0})

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	jpSegment, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}
	longWalkerSegment, err := segment.Save(ctx, pool, env, reg, "Long walkers", `total_distance_km >= 100.0`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	report, err := batch.Recompute(ctx, pool, redisClient, reg)
	if err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}
	if report.Generation != 1 {
		t.Errorf("Generation = %d, want 1", report.Generation)
	}

	jpBitmap, err := forward.CurrentBitmap(ctx, pool, jpSegment.ID)
	if err != nil {
		t.Fatalf("forward bitmap: %v", err)
	}
	jpOrdinal, err := ordinal.Lookup(ctx, pool, jpChannel)
	if err != nil {
		t.Fatalf("lookup jp ordinal: %v", err)
	}
	usOrdinal, err := ordinal.Lookup(ctx, pool, usChannel)
	if err != nil {
		t.Fatalf("lookup us ordinal: %v", err)
	}
	if !jpBitmap.Contains(uint32(jpOrdinal)) {
		t.Error("Japan segment forward index does not contain the JP channel")
	}
	if jpBitmap.Contains(uint32(usOrdinal)) {
		t.Error("Japan segment forward index contains the US channel")
	}
	if jpBitmap.GetCardinality() != 1 {
		t.Errorf("Japan segment cardinality = %d, want 1", jpBitmap.GetCardinality())
	}

	// Reverse index: the JP channel belongs to both segments (Japan, and long walkers at 150km);
	// the US channel belongs to neither.
	jpReverse, err := reverse.Get(ctx, redisClient, jpChannel)
	if err != nil {
		t.Fatalf("reverse.Get(jp): %v", err)
	}
	if jpReverse.GetCardinality() != 2 {
		t.Errorf("JP channel reverse cardinality = %d, want 2", jpReverse.GetCardinality())
	}
	if !jpReverse.Contains(uint32(jpSegment.Ordinal)) || !jpReverse.Contains(uint32(longWalkerSegment.Ordinal)) {
		t.Errorf("JP channel reverse bitmap = %v, want it to contain both segment ordinals", jpReverse.ToArray())
	}

	usReverse, err := reverse.Get(ctx, redisClient, usChannel)
	if err != nil {
		t.Fatalf("reverse.Get(us): %v", err)
	}
	if !usReverse.IsEmpty() {
		t.Errorf("US channel reverse bitmap = %v, want empty", usReverse.ToArray())
	}
}

// TestIncrementalUpdateFlipsBothIndexesWithoutABatchRun demonstrates CW-0005 Unit 5: a channel that
// starts outside a segment and gains the qualifying attribute is reflected in both indexes without
// waiting for the next batch.Recompute.
func TestIncrementalUpdateFlipsBothIndexesWithoutABatchRun(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "US"})

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	jpSegment, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}
	if jpSegment.RefreshMode != segment.RefreshIncremental {
		t.Fatalf("RefreshMode = %q, want incremental (no event aggregate referenced)", jpSegment.RefreshMode)
	}

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("initial batch.Recompute: %v", err)
	}

	before, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		t.Fatalf("reverse.Get (before): %v", err)
	}
	if before.Contains(uint32(jpSegment.Ordinal)) {
		t.Fatal("channel already in the Japan segment before the attribute change")
	}

	evaluator := eval.New(env)
	segments, err := segment.List(ctx, pool)
	if err != nil {
		t.Fatalf("segment.List: %v", err)
	}

	// The channel's country changes to JP — no batch run in between.
	if err := incremental.UpdateChannel(ctx, pool, redisClient, evaluator, channelID, segments,
		map[string]any{"country": "JP"},
	); err != nil {
		t.Fatalf("incremental.UpdateChannel: %v", err)
	}

	after, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		t.Fatalf("reverse.Get (after): %v", err)
	}
	if !after.Contains(uint32(jpSegment.Ordinal)) {
		t.Error("channel not in the Japan segment's reverse bitmap after the incremental update")
	}

	forwardBM, err := forward.CurrentBitmap(ctx, pool, jpSegment.ID)
	if err != nil {
		t.Fatalf("forward bitmap: %v", err)
	}
	ord, err := ordinal.Lookup(ctx, pool, channelID)
	if err != nil {
		t.Fatalf("lookup ordinal: %v", err)
	}
	if !forwardBM.Contains(uint32(ord)) {
		t.Error("channel not in the Japan segment's forward bitmap after the incremental update")
	}
}

// TestBatchOnlySegmentIsExcludedFromDependencyMap demonstrates CW-0005 Unit 5: a segment whose
// predicate reads an event aggregate is classified batch_only and never appears in the dependency
// map, since its membership can move without any attribute changing.
func TestBatchOnlySegmentIsExcludedFromDependencyMap(t *testing.T) {
	pool, _ := testDeps(t)
	ctx := context.Background()

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	seg, err := segment.Save(ctx, pool, env, reg, "Active walkers", `route_screen_views_7d >= 3.0`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}
	if seg.RefreshMode != segment.RefreshBatchOnly {
		t.Fatalf("RefreshMode = %q, want batch_only", seg.RefreshMode)
	}

	deps, err := incremental.DependencyMap([]*segment.Segment{seg}, reg)
	if err != nil {
		t.Fatalf("DependencyMap: %v", err)
	}
	if segs := deps["route_screen_views_7d"]; len(segs) != 0 {
		t.Errorf("dependency map lists %d segments for route_screen_views_7d, want 0 (batch_only excluded)", len(segs))
	}
}

// TestReconciliationReportsDisagreementAfterAnIncrementalDrift demonstrates CW-0005 Unit 6: when the
// incrementally-maintained index and a fresh full recomputation disagree, Recompute's report says
// so by segment.
func TestReconciliationReportsDisagreementAfterAnIncrementalDrift(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "JP"})

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	jpSegment, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("initial batch.Recompute: %v", err)
	}

	// Change the channel's real attribute value in Postgres without going through the incremental
	// path — simulating a missed event, the exact drift Unit 6 exists to catch.
	if _, err := pool.Exec(ctx,
		`UPDATE channels SET attributes = $1 WHERE id = $2`, map[string]any{"country": "US"}, channelID,
	); err != nil {
		t.Fatalf("update channel attributes: %v", err)
	}

	report, err := batch.Recompute(ctx, pool, redisClient, reg)
	if err != nil {
		t.Fatalf("second batch.Recompute: %v", err)
	}
	if report.Disagreements[jpSegment.ID] != 1 {
		t.Errorf("Disagreements[%s] = %d, want 1 (one channel left the segment)", jpSegment.ID, report.Disagreements[jpSegment.ID])
	}
}

// TestRetiredOrdinalIsNeverReused demonstrates CW-0005 Unit 1: a channel's ordinal is retired, not
// freed, so a later channel can never be assigned a retired ordinal and inherit its old membership.
func TestRetiredOrdinalIsNeverReused(t *testing.T) {
	pool, _ := testDeps(t)
	ctx := context.Background()

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "JP"})
	retiredOrdinal, err := ordinal.Lookup(ctx, pool, channelID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if err := ordinal.Retire(ctx, pool, channelID); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if _, err := ordinal.Lookup(ctx, pool, channelID); !errors.Is(err, ordinal.ErrRetired) {
		t.Fatalf("Lookup after retire: err = %v, want it to wrap ordinal.ErrRetired", err)
	}

	// Every newly allocated ordinal afterward must still be strictly greater than the retired one —
	// nextval() never goes backward, and the retired row keeps its number rather than freeing it.
	for i := 0; i < 5; i++ {
		newChannelID := insertChannel(t, ctx, pool, map[string]any{"country": "US"})
		newOrdinal, err := ordinal.Lookup(ctx, pool, newChannelID)
		if err != nil {
			t.Fatalf("Lookup(new channel %d): %v", i, err)
		}
		if newOrdinal <= retiredOrdinal {
			t.Errorf("new ordinal %d is not greater than the retired ordinal %d", newOrdinal, retiredOrdinal)
		}
	}
}

// TestRecomputeExcludesRetiredChannels demonstrates that a retired channel never appears in a
// freshly computed segment, even though its channel row (and its old bitmap membership) may still
// exist.
func TestRecomputeExcludesRetiredChannels(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()

	channelID := insertChannel(t, ctx, pool, map[string]any{"country": "JP"})

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	jpSegment, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	if err := ordinal.Retire(ctx, pool, channelID); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	bm, err := forward.CurrentBitmap(ctx, pool, jpSegment.ID)
	if err != nil {
		t.Fatalf("forward bitmap: %v", err)
	}
	if !bm.IsEmpty() {
		t.Errorf("forward bitmap = %v, want empty (the only matching channel is retired)", bm.ToArray())
	}
}
