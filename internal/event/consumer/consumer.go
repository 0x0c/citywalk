// Package consumer implements CW-0009 Unit 5's rollup consumer, in two variants that share the same
// rollup-writing logic (applyBatch and reconcileBudget) and differ only in where the next event comes
// from — CW-0009 Unit 3's "each consumer tracks its own position," realized two ways:
//
//   - RunOnce reads events_log since its last recorded position (event_consumer_offsets), applies
//     each event to the targeting rollup, the campaign rollup, the reach sketch, and the suppression
//     rollup (Units 5 and 7), and advances its offset — all inside one Postgres transaction.
//   - RunOnceFromLog reads from the Kafka-compatible log instead (internal/platform/eventlog, CW-0010
//     Unit 5), tracking its position as a Kafka consumer group's committed offsets — a second system
//     from the Postgres transaction that holds the rollup writes, unlike RunOnce's single transaction.
//
// Unit 3 requires every consumer to be idempotent under at-least-once delivery. Committing the offset
// advance and the rollup writes together, as RunOnce does, makes a crash between them unobservable for
// that consumer specifically, but the same argument does not extend to RunOnceFromLog, whose position
// lives in a system the Postgres transaction cannot include. applyBatch closes the gap at its actual
// source instead of leaving it to each consumer's position-tracking scheme: every rollup write it
// makes for an event is guarded by rollup_applied_events (migrations/0014_rollup_applied_events.sql),
// a table keyed by event id that records an event's rollup writes as made, in the same transaction as
// those writes. A replayed event — from either consumer, for any reason a batch gets reprocessed —
// finds its id already recorded and applyBatch skips its rollup writes entirely, rather than letting
// the unconditional `count = count + 1` these rollups use double-count it. Idempotency is therefore a
// property of the rollup writes themselves, shared by construction, not a guarantee each consumer has
// to separately re-derive from how it happens to track its own position.
package consumer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/hll"
	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/governance/budget"
)

// TargetingRollupConsumer is the fixed name of CW-0009's one built-in rollup consumer. A future
// consumer (e.g. the columnar export Unit 4 names as a third reader) registers its own name and
// advances independently, since each consumer tracks its own position.
const TargetingRollupConsumer = "rollup"

// campaignRollupKinds is the subset of impression-family kinds that carry a variant and so are
// counted in campaign_rollup; suppression and holdout-qualified events carry no variant (no variant
// was ever selected for either) and are counted in suppression_rollup instead.
var campaignRollupKinds = map[model.Kind]bool{
	model.KindImpression:  true,
	model.KindButtonPress: true,
	model.KindDismissal:   true,
	model.KindAutoClose:   true,
}

// storedEvent is the shape both this package's Postgres-backed consumer (RunOnce) and its
// Kafka-log-backed consumer (RunOnceFromLog) reduce their source event to before handing it to the
// rollup-writing logic below — the one part of a consumer the two are required to share, per CW-0009
// Unit 3: only where the next event comes from differs between them. Seq is meaningful only for
// RunOnce (events_log's receipt sequence, its position-tracking column); RunOnceFromLog leaves it
// zero, since the Kafka consumer group's own committed offset is its position instead.
type storedEvent struct {
	ID                string
	Seq               int64
	ChannelID         string
	Kind              model.Kind
	Name              string
	DeviceTime        time.Time
	ServerTime        time.Time
	MessageID         string
	VariantID         string
	SuppressionReason string
}

func (e storedEvent) eventName() string {
	if e.Kind == model.KindCustom {
		return e.Name
	}
	return string(e.Kind)
}

// bucketTime is the day/hour every rollup below counts e toward: model.EffectiveTime of e's two
// timestamps, which is e's device time unless Unit 1's clock-offset check says it cannot be trusted,
// in which case it is the server's own receipt time instead. Every rollup writer in this file must
// bucket by this, not by e.DeviceTime directly, or a device's clock — accidentally or deliberately
// wrong — can place a count in a bucket the server never actually reached.
func (e storedEvent) bucketTime() time.Time {
	return model.EffectiveTime(e.DeviceTime, e.ServerTime)
}

// RunOnce advances consumerName past every event currently in events_log, up to batchLimit rows,
// applying each to the rollups, and returns how many it processed. Zero means nothing new had
// arrived. Call it on a schedule (a cron, a background loop); CW-0009 does not itself define that
// schedule, since "how often" is an operational choice this pass leaves to the deployment.
//
// budgetCounter is CW-0007 Unit 5's reconciliation hook: when non-nil, every impression in the batch
// also increments the reporting channel's project-wide budget counter, which is what keeps the
// budget accurate against impressions the device actually delivered rather than only the ones this
// server happened to hand out through Confirm. Pass nil to skip it entirely (e.g. a deployment that
// hasn't wired CW-0007 in, or a test exercising CW-0009 in isolation). The Redis update happens after
// the transaction below commits, not inside it: Redis has no part in that transaction's atomicity,
// so recording it first and having the commit fail would count an impression this consumer never
// actually finished processing.
func RunOnce(ctx context.Context, pool *pgxpool.Pool, consumerName string, batchLimit int, budgetCounter *budget.Counter) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("consumer: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lastSeq, err := currentOffset(ctx, tx, consumerName)
	if err != nil {
		return 0, err
	}

	events, err := fetchSince(ctx, tx, lastSeq, batchLimit)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("consumer: commit: %w", err)
		}
		return 0, nil
	}

	if err := applyBatch(ctx, tx, events); err != nil {
		return 0, err
	}

	newSeq := events[len(events)-1].Seq
	if _, err := tx.Exec(ctx,
		`UPDATE event_consumer_offsets SET last_seq = $1, updated_at = now() WHERE consumer_name = $2`,
		newSeq, consumerName,
	); err != nil {
		return 0, fmt.Errorf("consumer: advance offset: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("consumer: commit: %w", err)
	}

	if err := reconcileBudget(ctx, budgetCounter, events); err != nil {
		return 0, err
	}

	return len(events), nil
}

// applyBatch applies every event in events to the targeting rollup, the campaign rollup, the reach
// sketch, and the suppression rollup (Units 5 and 7) inside tx — the rollup-writing logic RunOnce and
// RunOnceFromLog share verbatim, per CW-0009 Unit 3: only where events themselves come from is allowed
// to differ between a Postgres-backed and a Kafka-log-backed consumer.
//
// Before writing any rollup for e, it records e as applied (markApplied) and skips e entirely if that
// record already existed — Unit 3's idempotency requirement, implemented once here rather than by each
// consumer separately. One record per event is enough regardless of how many rollups e touches: which
// rollups an event drives (targeting always; campaign and the reach sketch only for an impression-family
// kind carrying a message id; suppression only for a suppression carrying one) is fully determined by
// that event's own kind and message id, both fixed at creation, so a replayed event resolves to the
// exact same set of writes as the first time and a single guard in front of all of them is sufficient.
func applyBatch(ctx context.Context, tx pgx.Tx, events []storedEvent) error {
	for _, e := range events {
		alreadyApplied, err := markApplied(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		if alreadyApplied {
			continue
		}

		if err := applyTargetingRollup(ctx, tx, e); err != nil {
			return err
		}
		if e.MessageID == "" {
			continue
		}
		switch {
		case campaignRollupKinds[e.Kind]:
			if err := applyCampaignRollup(ctx, tx, e); err != nil {
				return err
			}
			if e.Kind == model.KindImpression {
				if err := applyReachSketch(ctx, tx, e); err != nil {
					return err
				}
			}
		case e.Kind == model.KindSuppression:
			if err := applySuppressionRollup(ctx, tx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// markApplied inserts e's event id into rollup_applied_events, the per-event idempotency key every
// rollup write in applyBatch is guarded by (migrations/0014_rollup_applied_events.sql), and reports
// whether it was already present — a replay of an event this same transaction-scoped guarantee already
// covered once. It runs inside tx, the same transaction as the rollup writes it guards, so the record
// and the writes it protects always commit or roll back together.
func markApplied(ctx context.Context, tx pgx.Tx, eventID string) (alreadyApplied bool, err error) {
	tag, err := tx.Exec(ctx,
		`INSERT INTO rollup_applied_events (event_id) VALUES ($1) ON CONFLICT (event_id) DO NOTHING`,
		eventID,
	)
	if err != nil {
		return false, fmt.Errorf("consumer: mark event %s applied: %w", eventID, err)
	}
	return tag.RowsAffected() == 0, nil
}

// reconcileBudget folds every impression in events into budgetCounter, CW-0007 Unit 5's project-wide
// budget reconciliation — see RunOnce's doc comment for why this runs after the rollup transaction
// commits rather than inside it. budgetCounter may be nil, in which case this is a no-op.
func reconcileBudget(ctx context.Context, budgetCounter *budget.Counter, events []storedEvent) error {
	if budgetCounter == nil {
		return nil
	}
	for _, e := range events {
		if e.Kind != model.KindImpression {
			continue
		}
		if err := budgetCounter.RecordImpression(ctx, e.ChannelID, e.DeviceTime); err != nil {
			return fmt.Errorf("consumer: reconcile project budget for event %s: %w", e.ID, err)
		}
	}
	return nil
}

func currentOffset(ctx context.Context, tx pgx.Tx, consumerName string) (int64, error) {
	var lastSeq int64
	err := tx.QueryRow(ctx,
		`INSERT INTO event_consumer_offsets (consumer_name, last_seq) VALUES ($1, 0)
         ON CONFLICT (consumer_name) DO UPDATE SET consumer_name = EXCLUDED.consumer_name
         RETURNING last_seq`,
		consumerName,
	).Scan(&lastSeq)
	if err != nil {
		return 0, fmt.Errorf("consumer: read offset for %q: %w", consumerName, err)
	}
	return lastSeq, nil
}

func fetchSince(ctx context.Context, tx pgx.Tx, lastSeq int64, batchLimit int) ([]storedEvent, error) {
	rows, err := tx.Query(ctx,
		`SELECT seq, id::text, channel_id, kind, name, device_time, server_time,
                COALESCE(message_id::text, ''), COALESCE(variant_id::text, ''), COALESCE(suppression_reason, '')
         FROM events_log WHERE seq > $1 ORDER BY seq LIMIT $2`,
		lastSeq, batchLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("consumer: query events since %d: %w", lastSeq, err)
	}
	defer rows.Close()

	var events []storedEvent
	for rows.Next() {
		var e storedEvent
		if err := rows.Scan(
			&e.Seq, &e.ID, &e.ChannelID, &e.Kind, &e.Name, &e.DeviceTime, &e.ServerTime, &e.MessageID, &e.VariantID, &e.SuppressionReason,
		); err != nil {
			return nil, fmt.Errorf("consumer: scan event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("consumer: iterate events: %w", err)
	}
	return events, nil
}

func applyTargetingRollup(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO targeting_rollup (channel_id, event_name, day, count)
         VALUES ($1, $2, date_trunc('day', $3::timestamptz), 1)
         ON CONFLICT (channel_id, event_name, day) DO UPDATE SET count = targeting_rollup.count + 1`,
		e.ChannelID, e.eventName(), e.bucketTime(),
	)
	if err != nil {
		return fmt.Errorf("consumer: apply targeting rollup for event %s: %w", e.ID, err)
	}
	return nil
}

func applyCampaignRollup(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO campaign_rollup (message_id, variant_id, hour, kind, count)
         VALUES ($1, $2, date_trunc('hour', $3::timestamptz), $4, 1)
         ON CONFLICT (message_id, variant_id, hour, kind) DO UPDATE SET count = campaign_rollup.count + 1`,
		e.MessageID, e.VariantID, e.bucketTime(), string(e.Kind),
	)
	if err != nil {
		return fmt.Errorf("consumer: apply campaign rollup for event %s: %w", e.ID, err)
	}
	return nil
}

func applySuppressionRollup(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO suppression_rollup (message_id, reason, day, count)
         VALUES ($1, $2, date_trunc('day', $3::timestamptz), 1)
         ON CONFLICT (message_id, reason, day) DO UPDATE SET count = suppression_rollup.count + 1`,
		e.MessageID, e.SuppressionReason, e.bucketTime(),
	)
	if err != nil {
		return fmt.Errorf("consumer: apply suppression rollup for event %s: %w", e.ID, err)
	}
	return nil
}

// applyReachSketch folds e's channel into the HyperLogLog sketch for its message, variant, and day
// (Unit 5's unique-reach estimate), reading the existing sketch (if any), merging in the one new
// identity, and writing the result back.
func applyReachSketch(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	bucket := e.bucketTime()

	var existing []byte
	err := tx.QueryRow(ctx,
		`SELECT sketch FROM reach_sketch WHERE message_id = $1 AND variant_id = $2 AND day = date_trunc('day', $3::timestamptz)`,
		e.MessageID, e.VariantID, bucket,
	).Scan(&existing)

	var sketch *hll.Sketch
	switch err {
	case nil:
		sketch, err = hll.Unmarshal(existing)
		if err != nil {
			return fmt.Errorf("consumer: unmarshal reach sketch for event %s: %w", e.ID, err)
		}
	case pgx.ErrNoRows:
		sketch = hll.New()
	default:
		return fmt.Errorf("consumer: read reach sketch for event %s: %w", e.ID, err)
	}

	sketch.AddIdentity(e.ChannelID)

	_, err = tx.Exec(ctx,
		`INSERT INTO reach_sketch (message_id, variant_id, day, sketch)
         VALUES ($1, $2, date_trunc('day', $3::timestamptz), $4)
         ON CONFLICT (message_id, variant_id, day) DO UPDATE SET sketch = EXCLUDED.sketch`,
		e.MessageID, e.VariantID, bucket, sketch.Marshal(),
	)
	if err != nil {
		return fmt.Errorf("consumer: write reach sketch for event %s: %w", e.ID, err)
	}
	return nil
}
