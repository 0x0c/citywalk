package consumer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/governance/budget"
	"github.com/0x0c/citywalk/internal/platform/eventlog"
)

// LogRollupConsumerGroup is the fixed consumer group name for the log-backed counterpart of
// TargetingRollupConsumer. As with TargetingRollupConsumer, a future consumer registers its own group
// and advances independently — a Kafka consumer group already gives each group its own committed
// position per partition, which is what makes that independence free.
const LogRollupConsumerGroup = "citywalk-rollup"

// logPoller is the subset of *eventlog.Consumer RunOnceFromLog needs — narrow enough to fake in a
// unit test that has no broker to poll.
type logPoller interface {
	Poll(ctx context.Context) ([]eventlog.Record, error)
	Commit(ctx context.Context, batch []eventlog.Record) error
}

// RunOnceFromLog is RunOnce's log-backed counterpart: it polls logConsumer for whatever batch is
// currently available, applies every event to the rollups in one Postgres transaction (applyBatch —
// the same function RunOnce uses), commits that transaction, then commits logConsumer's consumer-group
// offsets past the batch, and returns how many events it processed. Zero means nothing new had
// arrived. budgetCounter behaves exactly as it does for RunOnce.
//
// The commit order — Postgres first, then the log's offsets — is what CW-0009 Unit 3's at-least-once
// delivery requires: a crash between the two leaves the consumer group's position behind, so the same
// batch is refetched and reprocessed on restart, rather than the offset advancing past events whose
// rollup writes never happened.
//
// That same ordering is also this consumer's open gap against Unit 3's "every consumer is required to
// be idempotent." RunOnce satisfies that requirement by committing its offset advance and its rollup
// writes in the same Postgres transaction, so a retry is never observable. Here the log's offset and
// the rollup writes are two different systems that can fail independently between the two commits
// above; applyBatch's rollup writes are `count = count + 1`, not keyed by event id, so a reprocessed
// batch increments those counts a second time rather than being recognized as a duplicate. Closing
// that gap needs its own idempotency key on the rollup writes themselves — out of scope here, since
// this pass shares applyBatch unchanged rather than modifying the one part of a consumer CW-0009
// Unit 3 requires the two variants to share, and no live broker is reachable in this repository's
// sandbox to exercise the gap either way.
func RunOnceFromLog(ctx context.Context, pool *pgxpool.Pool, logConsumer logPoller, budgetCounter *budget.Counter) (int, error) {
	records, err := logConsumer.Poll(ctx)
	if err != nil {
		return 0, fmt.Errorf("consumer: poll log: %w", err)
	}
	if len(records) == 0 {
		return 0, nil
	}

	events := make([]storedEvent, len(records))
	for i, r := range records {
		e, err := model.DecodeLogEvent(r.Value)
		if err != nil {
			return 0, fmt.Errorf("consumer: decode log record %d: %w", i, err)
		}
		events[i] = fromLogEvent(e)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("consumer: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := applyBatch(ctx, tx, events); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("consumer: commit: %w", err)
	}

	if err := logConsumer.Commit(ctx, records); err != nil {
		return 0, fmt.Errorf("consumer: commit log offsets: %w", err)
	}

	if err := reconcileBudget(ctx, budgetCounter, events); err != nil {
		return 0, err
	}

	return len(events), nil
}

// fromLogEvent reduces a decoded log event to the shape applyBatch needs. Seq stays zero: the log
// path's position is the consumer group's committed offset (tracked by eventlog.Consumer itself), not
// a per-event sequence number the way events_log's is.
func fromLogEvent(e model.Event) storedEvent {
	return storedEvent{
		ID:                e.ID,
		ChannelID:         e.ChannelID,
		Kind:              e.Kind,
		Name:              e.Name,
		DeviceTime:        e.DeviceTime,
		ServerTime:        e.ServerTime,
		MessageID:         e.MessageID,
		VariantID:         e.VariantID,
		SuppressionReason: string(e.SuppressionReason),
	}
}
