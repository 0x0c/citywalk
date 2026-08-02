package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/platform/eventlog"
)

// logProducer is the subset of *eventlog.Producer LogPublisher needs — narrow enough to substitute a
// fake in a unit test that has no broker to publish against.
type logProducer interface {
	Publish(ctx context.Context, key, value []byte) error
}

// LogPublisher is CW-0010 Unit 11's phase-two publisher: it appends each event to the
// Kafka-compatible log (internal/platform/eventlog) instead of writing events_log directly. It is
// selected only when internal/platform/config's event publisher mode is "log" — never the default,
// since the actual cutover from phase one is an operational decision CW-0010 Unit 11 defers.
//
// Unlike PostgresPublisher, the log does not deduplicate by event id at write time; CW-0009 Unit 4's
// merge-time dedup happens downstream, in whichever store eventually reads this log. Publish here
// therefore reports every event it successfully appended, not just the ones "newly accepted" a
// Postgres INSERT ... ON CONFLICT DO NOTHING can distinguish.
type LogPublisher struct {
	Producer logProducer
}

// Publish stamps now onto every event as its server-receipt time (mirroring PostgresPublisher's own
// appendToLog), then appends each one to the log in order, keyed by channel identifier (Unit 3's
// partitioning requirement — see eventlog.PartitionKey). It stops and returns the count already
// published, plus an error, on the first publish failure, rather than silently skipping the rest of
// the batch.
func (p LogPublisher) Publish(ctx context.Context, events []model.Event, now time.Time) (int, error) {
	for i, e := range events {
		e.ServerTime = now
		value, err := e.EncodeLog()
		if err != nil {
			return i, fmt.Errorf("ingest: encode event %s for log: %w", e.ID, err)
		}
		if err := p.Producer.Publish(ctx, eventlog.PartitionKey(e.ChannelID), value); err != nil {
			return i, fmt.Errorf("ingest: publish event %s to log: %w", e.ID, err)
		}
	}
	return len(events), nil
}
