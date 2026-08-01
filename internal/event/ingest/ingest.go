// Package ingest implements CW-0009 Unit 2: batch acceptance. Accept validates each event's shape,
// enforces the per-channel rate limit, appends the surviving events to the log (Unit 3's seam) with
// merge-time dedup on the event identifier (Unit 4), and returns counts. It performs no aggregation,
// no lookup against campaign state, and no deduplication beyond the identifier — which is what keeps
// acceptance latency independent of everything downstream.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/event/ratelimit"
)

// Result is Accept's per-batch tally, for the caller to ack against.
type Result struct {
	Accepted            int
	RejectedInvalid     int
	RejectedRateLimited int
}

// rejectedCounter records Unit 2's rejected-volume-by-channel metric: without it, a device stuck in
// a render loop degrades silently instead of showing up as a spike a project owner can see.
var rejectedCounter = mustCounter()

func mustCounter() metric.Int64Counter {
	c, err := otel.Meter("citywalk/event/ingest").Int64Counter(
		"citywalk.event.rejected_count",
		metric.WithDescription("Count of submitted events rejected before reaching the log, by channel and reason"),
	)
	if err != nil {
		// Int64Counter only fails on a malformed instrument name, fixed at compile time.
		panic(err)
	}
	return c
}

// Accept processes one channel's batch. Every event in batch must share channelID; a mixed batch is
// a shape error, since the rate limit and the rejection metric below are both scoped per channel.
func Accept(
	ctx context.Context, pool *pgxpool.Pool, limiter ratelimit.Limiter,
	channelID string, batch []model.Event, now time.Time,
) (Result, error) {
	var result Result
	valid := make([]model.Event, 0, len(batch))
	for _, e := range batch {
		if e.ChannelID != channelID {
			return Result{}, fmt.Errorf(
				"ingest: event %s has channel_id %q, want batch channel_id %q", e.ID, e.ChannelID, channelID,
			)
		}
		if err := e.Validate(); err != nil {
			result.RejectedInvalid++
			recordRejection(ctx, channelID, "invalid", 1)
			continue
		}
		valid = append(valid, e)
	}

	if len(valid) == 0 {
		return result, nil
	}

	allowed, err := limiter.Allow(ctx, channelID, int64(len(valid)), now)
	if err != nil {
		return Result{}, fmt.Errorf("ingest: rate limit check: %w", err)
	}
	if !allowed {
		result.RejectedRateLimited = len(valid)
		recordRejection(ctx, channelID, "rate_limited", len(valid))
		return result, nil
	}

	accepted, err := appendToLog(ctx, pool, valid, now)
	if err != nil {
		return Result{}, err
	}
	result.Accepted = accepted
	return result, nil
}

func recordRejection(ctx context.Context, channelID, reason string, n int) {
	rejectedCounter.Add(ctx, int64(n), metric.WithAttributes(
		attribute.String("citywalk.event.channel_id", channelID),
		attribute.String("citywalk.event.reject_reason", reason),
	))
}

// appendToLog inserts events into events_log in one batch, with a NO-OP on an id already present
// (Unit 4's merge-time dedup), and reports how many rows were actually new.
func appendToLog(ctx context.Context, pool *pgxpool.Pool, events []model.Event, now time.Time) (int, error) {
	batch := &pgx.Batch{}
	for _, e := range events {
		props := e.Properties
		if props == nil {
			props = map[string]any{}
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return 0, fmt.Errorf("ingest: marshal properties for event %s: %w", e.ID, err)
		}
		batch.Queue(
			`INSERT INTO events_log (id, channel_id, kind, name, device_time, server_time, properties, message_id, variant_id, suppression_reason)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
             ON CONFLICT (id) DO NOTHING`,
			e.ID, e.ChannelID, string(e.Kind), e.EventName(), e.DeviceTime, now, propsJSON,
			nullIfEmpty(e.MessageID), nullIfEmpty(e.VariantID), nullIfEmpty(string(e.SuppressionReason)),
		)
	}

	br := pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()

	accepted := 0
	for range events {
		tag, err := br.Exec()
		if err != nil {
			return 0, fmt.Errorf("ingest: append event: %w", err)
		}
		accepted += int(tag.RowsAffected())
	}
	return accepted, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
