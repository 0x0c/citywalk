// Package consumer implements CW-0009 Unit 5's rollup consumer: it reads events_log since its last
// recorded position (Unit 3), applies each event to the targeting rollup, the campaign rollup, the
// reach sketch, and the suppression rollup (Units 5 and 7), and advances its offset — all inside one
// transaction. That is this phase's answer to Unit 3's "every consumer is required to be idempotent":
// with a real partitioned log, idempotency has to be a property of each consumer's writes, because
// the log and the consumer's position are different systems that can fail independently. Here they
// are the same Postgres instance, so committing the rollup deltas and the offset advance together
// makes a crash between them impossible to observe — a retry after any failure reprocesses the same
// events rather than skipping or double-counting them.
package consumer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/hll"
	"github.com/0x0c/citywalk/internal/event/model"
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

type storedEvent struct {
	Seq               int64
	ChannelID         string
	Kind              model.Kind
	Name              string
	DeviceTime        time.Time
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

// RunOnce advances consumerName past every event currently in events_log, up to batchLimit rows,
// applying each to the rollups, and returns how many it processed. Zero means nothing new had
// arrived. Call it on a schedule (a cron, a background loop); CW-0009 does not itself define that
// schedule, since "how often" is an operational choice this pass leaves to the deployment.
func RunOnce(ctx context.Context, pool *pgxpool.Pool, consumerName string, batchLimit int) (int, error) {
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

	for _, e := range events {
		if err := applyTargetingRollup(ctx, tx, e); err != nil {
			return 0, err
		}
		if e.MessageID == "" {
			continue
		}
		switch {
		case campaignRollupKinds[e.Kind]:
			if err := applyCampaignRollup(ctx, tx, e); err != nil {
				return 0, err
			}
			if e.Kind == model.KindImpression {
				if err := applyReachSketch(ctx, tx, e); err != nil {
					return 0, err
				}
			}
		case e.Kind == model.KindSuppression:
			if err := applySuppressionRollup(ctx, tx, e); err != nil {
				return 0, err
			}
		}
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
	return len(events), nil
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
		`SELECT seq, channel_id, kind, name, device_time,
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
			&e.Seq, &e.ChannelID, &e.Kind, &e.Name, &e.DeviceTime, &e.MessageID, &e.VariantID, &e.SuppressionReason,
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
		e.ChannelID, e.eventName(), e.DeviceTime,
	)
	if err != nil {
		return fmt.Errorf("consumer: apply targeting rollup for event seq %d: %w", e.Seq, err)
	}
	return nil
}

func applyCampaignRollup(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO campaign_rollup (message_id, variant_id, hour, kind, count)
         VALUES ($1, $2, date_trunc('hour', $3::timestamptz), $4, 1)
         ON CONFLICT (message_id, variant_id, hour, kind) DO UPDATE SET count = campaign_rollup.count + 1`,
		e.MessageID, e.VariantID, e.DeviceTime, string(e.Kind),
	)
	if err != nil {
		return fmt.Errorf("consumer: apply campaign rollup for event seq %d: %w", e.Seq, err)
	}
	return nil
}

func applySuppressionRollup(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO suppression_rollup (message_id, reason, day, count)
         VALUES ($1, $2, date_trunc('day', $3::timestamptz), 1)
         ON CONFLICT (message_id, reason, day) DO UPDATE SET count = suppression_rollup.count + 1`,
		e.MessageID, e.SuppressionReason, e.DeviceTime,
	)
	if err != nil {
		return fmt.Errorf("consumer: apply suppression rollup for event seq %d: %w", e.Seq, err)
	}
	return nil
}

// applyReachSketch folds e's channel into the HyperLogLog sketch for its message, variant, and day
// (Unit 5's unique-reach estimate), reading the existing sketch (if any), merging in the one new
// identity, and writing the result back.
func applyReachSketch(ctx context.Context, tx pgx.Tx, e storedEvent) error {
	var existing []byte
	err := tx.QueryRow(ctx,
		`SELECT sketch FROM reach_sketch WHERE message_id = $1 AND variant_id = $2 AND day = date_trunc('day', $3::timestamptz)`,
		e.MessageID, e.VariantID, e.DeviceTime,
	).Scan(&existing)

	var sketch *hll.Sketch
	switch err {
	case nil:
		sketch, err = hll.Unmarshal(existing)
		if err != nil {
			return fmt.Errorf("consumer: unmarshal reach sketch for event seq %d: %w", e.Seq, err)
		}
	case pgx.ErrNoRows:
		sketch = hll.New()
	default:
		return fmt.Errorf("consumer: read reach sketch for event seq %d: %w", e.Seq, err)
	}

	sketch.AddIdentity(e.ChannelID)

	_, err = tx.Exec(ctx,
		`INSERT INTO reach_sketch (message_id, variant_id, day, sketch)
         VALUES ($1, $2, date_trunc('day', $3::timestamptz), $4)
         ON CONFLICT (message_id, variant_id, day) DO UPDATE SET sketch = EXCLUDED.sketch`,
		e.MessageID, e.VariantID, e.DeviceTime, sketch.Marshal(),
	)
	if err != nil {
		return fmt.Errorf("consumer: write reach sketch for event seq %d: %w", e.Seq, err)
	}
	return nil
}
