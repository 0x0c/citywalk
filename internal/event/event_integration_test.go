//go:build integration

// Run with: go test -tags=integration ./internal/event/... with CITYWALK_TEST_POSTGRES_DSN and
// CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package event_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/event/attribution"
	"github.com/0x0c/citywalk/internal/event/consumer"
	"github.com/0x0c/citywalk/internal/event/ingest"
	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/event/ratelimit"
	"github.com/0x0c/citywalk/internal/event/report"
	"github.com/0x0c/citywalk/internal/governance/budget"
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

	for _, table := range []string{
		"conversion_attributions", "suppression_rollup", "reach_sketch", "campaign_rollup",
		"targeting_rollup", "event_consumer_offsets", "events_log",
		"variants", "control_policies", "triggers", "display_conditions", "message_audit_log", "delivery_change_log", "messages", "channels",
	} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	return pool, redisClient
}

func insertChannel(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	return id
}

// insertMessage creates a minimal message and one variant (satisfying events_log's foreign keys)
// and returns their IDs.
func insertMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (messageID, variantID string) {
	t.Helper()
	now := time.Now()
	if err := pool.QueryRow(ctx, `
		INSERT INTO messages (name, state, priority, window_start, window_end)
		VALUES ('test', 'active', 0, $1, $2)
		RETURNING id
	`, now.Add(-time.Hour), now.Add(time.Hour)).Scan(&messageID); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO variants (message_id, weight, language, content)
		VALUES ($1, 100, 'en', '{}') RETURNING id
	`, messageID).Scan(&variantID); err != nil {
		t.Fatalf("insert variant: %v", err)
	}
	return messageID, variantID
}

// TestAcceptDedupsAndAggregates is the end-to-end proof of CW-0009 Units 1 through 5: a batch of
// events is accepted, a resend of the same batch is a no-op (Unit 4), and the rollup consumer turns
// the accepted events into the targeting and campaign rollups (Unit 5) a real caller would read.
func TestAcceptDedupsAndAggregates(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	messageID, variantID := insertMessage(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	batch := []model.Event{
		{
			ID: "01912d2c-0000-7000-8000-000000000001", ChannelID: channelID,
			Kind: model.KindImpression, MessageID: messageID, VariantID: variantID, DeviceTime: now,
		},
		{
			ID: "01912d2c-0000-7000-8000-000000000002", ChannelID: channelID,
			Kind: model.KindCustom, Name: "screen_view", DeviceTime: now,
		},
	}

	result, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if result.Accepted != 2 || result.RejectedInvalid != 0 || result.RejectedRateLimited != 0 {
		t.Fatalf("Accept() = %+v, want 2 accepted, 0 rejected", result)
	}

	// A resend of the identical batch must be a no-op: the events already exist, so Unit 4's
	// merge-time dedup collapses every row and nothing new is accepted.
	resend, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now)
	if err != nil {
		t.Fatalf("Accept (resend): %v", err)
	}
	if resend.Accepted != 0 {
		t.Errorf("Accept() on resend = %d accepted, want 0 (deduplicated)", resend.Accepted)
	}

	budgetCounter := &budget.Counter{Redis: redisClient, Window: 24 * time.Hour}
	processed, err := consumer.RunOnce(ctx, pool, consumer.TargetingRollupConsumer, 100, budgetCounter)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if processed != 2 {
		t.Fatalf("RunOnce() processed %d, want 2", processed)
	}

	var targetingCount int64
	if err := pool.QueryRow(ctx,
		`SELECT count FROM targeting_rollup WHERE channel_id = $1 AND event_name = 'screen_view'`, channelID,
	).Scan(&targetingCount); err != nil {
		t.Fatalf("query targeting_rollup: %v", err)
	}
	if targetingCount != 1 {
		t.Errorf("targeting_rollup screen_view count = %d, want 1", targetingCount)
	}

	reports, err := report.Variants(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("report.Variants: %v", err)
	}
	if len(reports) != 1 || reports[0].Impressions != 1 {
		t.Errorf("report.Variants() = %+v, want one variant with 1 impression", reports)
	}

	// CW-0007 Unit 5's reconciliation: the one impression in this batch must have been folded into
	// channelID's project budget counter by RunOnce, even though nothing ever called Confirm.
	remaining, err := budgetCounter.Remaining(ctx, channelID, 2, now)
	if err != nil {
		t.Fatalf("budgetCounter.Remaining: %v", err)
	}
	if remaining != 1 {
		t.Errorf("budget remaining after one reconciled impression = %d, want 1 (cap 2 minus 1)", remaining)
	}

	// Running the consumer again with nothing new to process must advance nothing further and
	// report zero processed — the idempotency Unit 3 requires of every consumer.
	again, err := consumer.RunOnce(ctx, pool, consumer.TargetingRollupConsumer, 100, budgetCounter)
	if err != nil {
		t.Fatalf("RunOnce (again): %v", err)
	}
	if again != 0 {
		t.Errorf("RunOnce() on an already-caught-up consumer processed %d, want 0", again)
	}
}

// TestRunOnceClampsFutureDeviceTimeToServerTime is CW-0009 Unit 1's clamp rule end to end: a device
// whose clock reads three days into the future must not be able to inflate a rollup bucket for a day
// the server has not reached yet — the targeting rollup row lands on the day Accept actually received
// the event (server_time, via now), not the day the device claims. Accept must also flag the event
// (Result.ClockSkewFlagged) rather than trust it verbatim, without rejecting it outright.
func TestRunOnceClampsFutureDeviceTimeToServerTime(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	futureDeviceTime := now.Add(72 * time.Hour)

	batch := []model.Event{
		{
			ID: "01912d2c-0000-7000-8000-000000000040", ChannelID: channelID,
			Kind: model.KindCustom, Name: "screen_view", DeviceTime: futureDeviceTime,
		},
	}

	result, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("Accept() = %+v, want 1 accepted", result)
	}
	if result.ClockSkewFlagged != 1 {
		t.Errorf("Accept() ClockSkewFlagged = %d, want 1 (a device clock 72h into the future is implausible)", result.ClockSkewFlagged)
	}

	if _, err := consumer.RunOnce(ctx, pool, consumer.TargetingRollupConsumer, 100, nil); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var day time.Time
	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT day, count FROM targeting_rollup WHERE channel_id = $1 AND event_name = 'screen_view'`, channelID,
	).Scan(&day, &count); err != nil {
		t.Fatalf("query targeting_rollup: %v", err)
	}
	if count != 1 {
		t.Errorf("targeting_rollup screen_view count = %d, want 1", count)
	}
	wantDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !day.Equal(wantDay) {
		t.Errorf("targeting_rollup day = %v, want %v (now's day, clamped from the device's claimed future day %v)", day, wantDay, futureDeviceTime)
	}
}

// TestAcceptFlagsButDoesNotRejectAnImplausiblyOldDeviceTime is the past-direction half of Unit 1's
// clock-offset sanity bound: a device time far older than any real offline backlog (here, a year) is
// still accepted — the event is not lost, and its raw device time is preserved for display or
// debugging — but it is both flagged (Result.ClockSkewFlagged) and not trusted for bucketing, which
// TestRunOnceClampsImplausiblyOldDeviceTimeToServerTime below proves separately.
func TestAcceptFlagsButDoesNotRejectAnImplausiblyOldDeviceTime(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ancientDeviceTime := now.Add(-365 * 24 * time.Hour)

	batch := []model.Event{
		{
			ID: "01912d2c-0000-7000-8000-000000000041", ChannelID: channelID,
			Kind: model.KindCustom, Name: "screen_view", DeviceTime: ancientDeviceTime,
		},
	}

	result, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("Accept() = %+v, want 1 accepted (an implausible clock is flagged, not rejected)", result)
	}
	if result.ClockSkewFlagged != 1 {
		t.Errorf("Accept() ClockSkewFlagged = %d, want 1 (a device clock a year in the past is implausible)", result.ClockSkewFlagged)
	}

	var storedDeviceTime time.Time
	if err := pool.QueryRow(ctx,
		`SELECT device_time FROM events_log WHERE channel_id = $1`, channelID,
	).Scan(&storedDeviceTime); err != nil {
		t.Fatalf("query events_log: %v", err)
	}
	if !storedDeviceTime.Equal(ancientDeviceTime) {
		t.Errorf("stored device_time = %v, want %v (the raw device time is preserved for display/debugging)", storedDeviceTime, ancientDeviceTime)
	}
}

// TestRunOnceClampsImplausiblyOldDeviceTimeToServerTime is the past-direction half of Unit 1's clamp
// rule end to end: an event whose device time is implausibly old (not merely a late-but-real offline
// backlog) buckets by the server's receipt time, not the claimed device day — the same protection
// TestRunOnceClampsFutureDeviceTimeToServerTime proves for the future direction.
func TestRunOnceClampsImplausiblyOldDeviceTimeToServerTime(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ancientDeviceTime := now.Add(-365 * 24 * time.Hour)

	batch := []model.Event{
		{
			ID: "01912d2c-0000-7000-8000-000000000042", ChannelID: channelID,
			Kind: model.KindCustom, Name: "screen_view", DeviceTime: ancientDeviceTime,
		},
	}

	if _, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if _, err := consumer.RunOnce(ctx, pool, consumer.TargetingRollupConsumer, 100, nil); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var day time.Time
	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT day, count FROM targeting_rollup WHERE channel_id = $1 AND event_name = 'screen_view'`, channelID,
	).Scan(&day, &count); err != nil {
		t.Fatalf("query targeting_rollup: %v", err)
	}
	if count != 1 {
		t.Errorf("targeting_rollup screen_view count = %d, want 1", count)
	}
	wantDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !day.Equal(wantDay) {
		t.Errorf("targeting_rollup day = %v, want %v (now's day, clamped from the device's implausible claimed day %v)", day, wantDay, ancientDeviceTime)
	}
}

// TestAcceptRejectsInvalidEventsButKeepsValidOnes confirms Unit 2's per-event rejection: one bad
// event in a batch does not sink the rest.
func TestAcceptRejectsInvalidEventsButKeepsValidOnes(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Now()

	batch := []model.Event{
		// Missing message_id: an impression is invalid without one.
		{ID: "01912d2c-0000-7000-8000-000000000010", ChannelID: channelID, Kind: model.KindImpression, DeviceTime: now},
		{ID: "01912d2c-0000-7000-8000-000000000011", ChannelID: channelID, Kind: model.KindCustom, Name: "launch", DeviceTime: now},
	}

	result, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if result.Accepted != 1 || result.RejectedInvalid != 1 {
		t.Fatalf("Accept() = %+v, want 1 accepted and 1 rejected as invalid", result)
	}
}

// TestAcceptEnforcesThePerChannelRateLimit confirms Unit 2's rate limit rejects a batch that would
// push a channel over its ceiling within the window, without touching the log.
func TestAcceptEnforcesThePerChannelRateLimit(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1, Window: time.Minute}
	now := time.Now()

	batch := []model.Event{
		{ID: "01912d2c-0000-7000-8000-000000000020", ChannelID: channelID, Kind: model.KindCustom, Name: "a", DeviceTime: now},
		{ID: "01912d2c-0000-7000-8000-000000000021", ChannelID: channelID, Kind: model.KindCustom, Name: "b", DeviceTime: now},
	}

	result, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if result.Accepted != 0 || result.RejectedRateLimited != 2 {
		t.Fatalf("Accept() = %+v, want 0 accepted and 2 rejected as rate limited", result)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events_log WHERE channel_id = $1`, channelID).Scan(&count); err != nil {
		t.Fatalf("count events_log: %v", err)
	}
	if count != 0 {
		t.Errorf("events_log has %d rows for a rate-limited batch, want 0", count)
	}
}

// TestAttributionPicksTheMostRecentPrecedingImpressionAndCountsTheHoldout is CW-0009 Unit 6's
// central property end to end: a channel shown variant A, then variant B, converts once —
// attribution goes to B (the most recent exposure), and a separate channel that qualified for the
// holdout and later converts counts toward the holdout's own rate.
func TestAttributionPicksTheMostRecentPrecedingImpressionAndCountsTheHoldout(t *testing.T) {
	pool, _ := testDeps(t)
	ctx := context.Background()
	messageID, variantA := insertMessage(t, ctx, pool)
	_, variantB := insertMessage(t, ctx, pool) // second message row unused; only its variant matters here
	channelA := insertChannel(t, ctx, pool)
	channelHoldout := insertChannel(t, ctx, pool)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insertRawEvent(t, ctx, pool, channelA, "impression", "", base, messageID, variantA, "")
	insertRawEvent(t, ctx, pool, channelA, "impression", "", base.Add(10*time.Minute), messageID, variantB, "")
	insertRawEvent(t, ctx, pool, channelA, "custom", "purchase", base.Add(20*time.Minute), "", "", "")

	insertRawEvent(t, ctx, pool, channelHoldout, "holdout_qualified", "", base, messageID, "", "")
	insertRawEvent(t, ctx, pool, channelHoldout, "custom", "purchase", base.Add(15*time.Minute), "", "", "")

	if err := attribution.Run(ctx, pool, messageID, "purchase", time.Hour); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Re-running over the same events must not double count (Unit 6's idempotency).
	if err := attribution.Run(ctx, pool, messageID, "purchase", time.Hour); err != nil {
		t.Fatalf("Run (again): %v", err)
	}

	counts, err := attribution.Counts(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts[variantB] != 1 {
		t.Errorf("counts[variantB] = %d, want 1 (the conversion attributes to the most recent impression)", counts[variantB])
	}
	if counts[variantA] != 0 {
		t.Errorf("counts[variantA] = %d, want 0 (only the most recent impression is credited)", counts[variantA])
	}
	if counts[attribution.HoldoutVariantID] != 1 {
		t.Errorf("counts[HoldoutVariantID] = %d, want 1", counts[attribution.HoldoutVariantID])
	}
}

// TestSuppressionReportBreaksDownByReason is CW-0009 Unit 7 end to end: suppression events aggregate
// by reason, alongside the impression count already in campaign_rollup.
func TestSuppressionReportBreaksDownByReason(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	messageID, variantID := insertMessage(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Now()

	batch := []model.Event{
		{ID: "01912d2c-0000-7000-8000-000000000030", ChannelID: channelID, Kind: model.KindImpression, MessageID: messageID, VariantID: variantID, DeviceTime: now},
		{ID: "01912d2c-0000-7000-8000-000000000031", ChannelID: channelID, Kind: model.KindSuppression, MessageID: messageID, SuppressionReason: model.ReasonCooldown, DeviceTime: now},
		{ID: "01912d2c-0000-7000-8000-000000000032", ChannelID: channelID, Kind: model.KindSuppression, MessageID: messageID, SuppressionReason: model.ReasonCooldown, DeviceTime: now},
		{ID: "01912d2c-0000-7000-8000-000000000033", ChannelID: channelID, Kind: model.KindSuppression, MessageID: messageID, SuppressionReason: model.ReasonProjectBudget, DeviceTime: now},
	}
	if _, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if _, err := consumer.RunOnce(ctx, pool, consumer.TargetingRollupConsumer, 100, nil); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	breakdown, err := report.Suppressions(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("Suppressions: %v", err)
	}
	if breakdown.Impressions != 1 {
		t.Errorf("Impressions = %d, want 1", breakdown.Impressions)
	}
	if breakdown.ByReason[string(model.ReasonCooldown)] != 2 {
		t.Errorf("ByReason[cooldown] = %d, want 2", breakdown.ByReason[string(model.ReasonCooldown)])
	}
	if breakdown.ByReason[string(model.ReasonProjectBudget)] != 1 {
		t.Errorf("ByReason[project_budget] = %d, want 1", breakdown.ByReason[string(model.ReasonProjectBudget)])
	}
}

// TestSuppressionRollupBucketsByReceiptTimeNotRawDeviceTime is Unit 1's two-timestamp rule applied to
// the suppression rollup specifically: a suppression event with a clock-tampered future device time
// must land in server-time's day, exactly like the targeting and campaign rollups already do — the
// suppression rollup must not be the one rollup writer that trusts device_time verbatim.
func TestSuppressionRollupBucketsByReceiptTimeNotRawDeviceTime(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	channelID := insertChannel(t, ctx, pool)
	messageID, _ := insertMessage(t, ctx, pool)
	limiter := ratelimit.Limiter{Redis: redisClient, Limit: 1000, Window: time.Minute}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	futureDeviceTime := now.Add(72 * time.Hour)

	batch := []model.Event{
		{
			ID: "01912d2c-0000-7000-8000-000000000034", ChannelID: channelID, Kind: model.KindSuppression,
			MessageID: messageID, SuppressionReason: model.ReasonCooldown, DeviceTime: futureDeviceTime,
		},
	}
	if _, err := ingest.Accept(ctx, pool, limiter, channelID, batch, now); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if _, err := consumer.RunOnce(ctx, pool, consumer.TargetingRollupConsumer, 100, nil); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var day time.Time
	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT day, count FROM suppression_rollup WHERE message_id = $1 AND reason = $2`,
		messageID, string(model.ReasonCooldown),
	).Scan(&day, &count); err != nil {
		t.Fatalf("query suppression_rollup: %v", err)
	}
	if count != 1 {
		t.Errorf("suppression_rollup count = %d, want 1", count)
	}
	wantDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !day.Equal(wantDay) {
		t.Errorf("suppression_rollup day = %v, want %v (now's day, clamped from the device's claimed future day %v)", day, wantDay, futureDeviceTime)
	}
}

// insertRawEvent writes directly to events_log, bypassing Accept, so attribution and report tests
// can set up fixtures (including holdout_qualified events, which Accept's own callers never submit
// on the device-facing path since a device does not observe its own holdout membership).
func insertRawEvent(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	channelID, kind, name string, deviceTime time.Time, messageID, variantID, suppressionReason string,
) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO events_log (id, channel_id, kind, name, device_time, message_id, variant_id, suppression_reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
	`, channelID, kind, name, deviceTime, nullIfEmptyStr(messageID), nullIfEmptyStr(variantID), nullIfEmptyStr(suppressionReason))
	if err != nil {
		t.Fatalf("insert raw event: %v", err)
	}
}

func nullIfEmptyStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
