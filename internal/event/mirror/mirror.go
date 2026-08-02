// Package mirror implements CW-0010 Unit 6 and CW-0009 Unit 4's alternate storage path: a periodic
// job that reads recently-accepted events out of events_log and writes them into ClickHouse
// (internal/platform/clickhouse), when CW-0010 Unit 11's configuration flag enables it.
//
// This package is additive, not a replacement for internal/event/consumer's rollup writer, which
// stays exactly as it is and is what devices' delivery depends on. Mirroring reads events_log through
// its own consumer offset (MirrorConsumer, a row in event_consumer_offsets distinct from
// consumer.TargetingRollupConsumer's), so it can fall behind, be replayed, or be disabled entirely
// without touching the rollups at all. See CW-0010 Unit 11's "staged adoption" text for why this
// stays off by default: citywalk has no real production traffic yet, so ClickHouse must not become
// the active reporting store until an operator decides otherwise.
package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/platform/clickhouse"
)

// MirrorConsumer is this job's own name in event_consumer_offsets — distinct from
// consumer.TargetingRollupConsumer, so mirroring falls behind or is replayed independently of the
// rollup writer that is the active default path.
const MirrorConsumer = "clickhouse_mirror"

// batchLimit bounds how many events one RunOnce call reads from events_log, matching
// internal/event/consumer's own rollupRecomputeBatchLimit for the same reason: a bounded batch keeps
// one ClickHouse insert short even under a burst backlog, while MirrorWorker.Work's drain loop still
// clears an arbitrarily large backlog within one firing by calling RunOnce repeatedly.
const batchLimit = 1000

// loggedEvent is one events_log row, read directly by this package rather than reusing
// internal/event/consumer's unexported storedEvent: the two packages read the same table
// independently, through their own offsets, and neither depends on the other's internals — the
// separation CW-0010 Unit 11 asks for between the active rollup path and this additive one.
type loggedEvent struct {
	Seq               int64
	ID                string
	ChannelID         string
	Kind              string
	Name              string
	DeviceTime        time.Time
	ServerTime        time.Time
	Properties        string
	MessageID         string
	VariantID         string
	SuppressionReason string
}

// RunOnce reads consumerName's backlog in events_log (up to batchLimit rows), converts it to
// clickhouse.RawEvent, and writes it to ClickHouse in one batch, advancing the Postgres offset only
// after the ClickHouse write succeeds. It returns how many events it mirrored; zero means nothing new
// had arrived.
//
// Unlike internal/event/consumer.RunOnce, the ClickHouse write and the offset advance cannot share
// one Postgres transaction — they are different systems — so this call is at-least-once, not
// exactly-once: a crash between a successful ClickHouse write and the offset advance re-mirrors the
// same events on the next run. That is safe by construction, not by accident: events_raw's
// ReplacingMergeTree engine (internal/platform/clickhouse/schema.go) collapses a resend to one row at
// merge time, which is exactly CW-0009 Unit 4's own design for tolerating resends without a
// read-before-write check anywhere on this path.
func RunOnce(ctx context.Context, pool *pgxpool.Pool, client *clickhouse.Client, consumerName string) (int, error) {
	lastSeq, err := currentOffset(ctx, pool, consumerName)
	if err != nil {
		return 0, err
	}

	events, err := fetchSince(ctx, pool, lastSeq, batchLimit)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}

	rows := make([]clickhouse.RawEvent, 0, len(events))
	for _, e := range events {
		row, err := clickhouse.LoggedEvent{
			ID:                e.ID,
			ChannelID:         e.ChannelID,
			Kind:              e.Kind,
			Name:              e.Name,
			DeviceTime:        e.DeviceTime,
			ServerTime:        e.ServerTime,
			Properties:        e.Properties,
			MessageID:         e.MessageID,
			VariantID:         e.VariantID,
			SuppressionReason: e.SuppressionReason,
		}.ToRawEvent()
		if err != nil {
			return 0, fmt.Errorf("mirror: convert event seq %d: %w", e.Seq, err)
		}
		rows = append(rows, row)
	}

	if err := client.InsertEvents(ctx, rows); err != nil {
		return 0, fmt.Errorf("mirror: insert into clickhouse: %w", err)
	}

	newSeq := events[len(events)-1].Seq
	if _, err := pool.Exec(ctx,
		`UPDATE event_consumer_offsets SET last_seq = $1, updated_at = now() WHERE consumer_name = $2`,
		newSeq, consumerName,
	); err != nil {
		return 0, fmt.Errorf("mirror: advance offset: %w", err)
	}

	return len(events), nil
}

func currentOffset(ctx context.Context, pool *pgxpool.Pool, consumerName string) (int64, error) {
	var lastSeq int64
	err := pool.QueryRow(ctx,
		`INSERT INTO event_consumer_offsets (consumer_name, last_seq) VALUES ($1, 0)
         ON CONFLICT (consumer_name) DO UPDATE SET consumer_name = EXCLUDED.consumer_name
         RETURNING last_seq`,
		consumerName,
	).Scan(&lastSeq)
	if err != nil {
		return 0, fmt.Errorf("mirror: read offset for %q: %w", consumerName, err)
	}
	return lastSeq, nil
}

func fetchSince(ctx context.Context, pool *pgxpool.Pool, lastSeq int64, limit int) ([]loggedEvent, error) {
	rows, err := pool.Query(ctx,
		`SELECT seq, id::text, channel_id::text, kind, name, device_time, server_time, properties::text,
                COALESCE(message_id::text, ''), COALESCE(variant_id::text, ''), COALESCE(suppression_reason, '')
         FROM events_log WHERE seq > $1 ORDER BY seq LIMIT $2`,
		lastSeq, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("mirror: query events since %d: %w", lastSeq, err)
	}
	defer rows.Close()

	var events []loggedEvent
	for rows.Next() {
		var e loggedEvent
		if err := rows.Scan(
			&e.Seq, &e.ID, &e.ChannelID, &e.Kind, &e.Name, &e.DeviceTime, &e.ServerTime, &e.Properties,
			&e.MessageID, &e.VariantID, &e.SuppressionReason,
		); err != nil {
			return nil, fmt.Errorf("mirror: scan event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mirror: iterate events: %w", err)
	}
	return events, nil
}
