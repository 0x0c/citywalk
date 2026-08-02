package clickhouse

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// tableName is events_raw's name: CW-0009 Unit 4's raw event store, the "events land in a columnar
// store" half of that unit's design. Unit 4's rollup tables (Unit 5's targeting and campaign rollups,
// re-expressed against ClickHouse) are not part of this pass — see this repository's CW-0010 and
// CW-0009 roadmap progress notes for what remains against Unit 6 and Unit 4.
const tableName = "events_raw"

// RawEventRetentionMonths is CW-0010 Unit 6's own retention figure for raw events: "13 months...
// chosen so a report can compare a campaign against the same period a year earlier."
const RawEventRetentionMonths = 13

// RawEventsDDL returns the CREATE TABLE statement for events_raw.
//
// Engine: ReplacingMergeTree(server_time) is CW-0009 Unit 4's merge-time deduplication, "which the
// engine provides directly rather than as application logic" (CW-0010 Unit 6) — rows that collapse to
// the same ORDER BY key are resolved during a background merge by keeping the one with the greatest
// server_time, never by a read-before-write check on any write path. This is the actual mechanism
// Unit 4 calls for, which Postgres's ON CONFLICT DO NOTHING (migrations/0006_events.sql,
// internal/event/ingest.appendToLog) could only approximate as a write-time constraint.
//
// Order: (message_id, device_time, id) is Unit 4's "ordered within a partition by project, message,
// and time — the order that campaign reports scan." No entity named "project" exists in this codebase
// yet (internal/definition/model has none — campaign identity here is a message and its variants), so
// this follows Unit 4's own fallback to message and time. id is appended last rather than used as the
// sole sort key: appending it is what makes the ReplacingMergeTree collapse specific to a genuine
// resend (identical id, and therefore identical message_id and device_time, since neither changes
// between a device's retries of the same event) rather than merging any two unrelated events that
// merely land in the same message/time bucket. message_id and variant_id are plain UUID columns, not
// Nullable(UUID): ClickHouse's own guidance is to avoid Nullable columns in a sorting key, so an event
// outside the impression family (Unit 1: MessageID/VariantID apply only to that family) is stored
// with the zero UUID instead of NULL — row.go's LoggedEvent.ToRawEvent makes that substitution, the
// same shape internal/event/model.Event already uses (an empty string for "no message").
//
// Partition: PARTITION BY toDate(device_time) is the "partitioned by day" this unit's own design text
// calls for.
//
// TTL: device_time plus 13 months is Unit 6's retention figure, expressed directly as a TTL clause
// rather than left to an operator's manual housekeeping — ClickHouse enforces it during background
// merges, dropping whole partitions once every row in them has aged out.
func RawEventsDDL() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s
(
    id                 UUID,
    channel_id         UUID,
    kind               LowCardinality(String),
    name               String,
    device_time        DateTime64(3, 'UTC'),
    server_time        DateTime64(3, 'UTC'),
    properties         String,
    message_id         UUID,
    variant_id         UUID,
    suppression_reason LowCardinality(String)
)
ENGINE = ReplacingMergeTree(server_time)
PARTITION BY toDate(device_time)
ORDER BY (message_id, device_time, id)
TTL device_time + INTERVAL %d MONTH`, tableName, RawEventRetentionMonths)
}

// EnsureSchema creates events_raw if it does not already exist. New calls this so a fresh ClickHouse
// instance is ready the first time internal/event/mirror's periodic job runs against it. There is no
// separate migration runner for ClickHouse the way internal/platform/postgres.Migrate versions
// PostgreSQL's schema across releases (CW-0010 Unit 3's migration-before-code rule): this pass adds
// exactly one table, and an idempotent CREATE TABLE IF NOT EXISTS is enough for it. A schema change
// serious enough to need real migration machinery is a decision for whichever pass makes ClickHouse
// the active reporting store, not this one.
func EnsureSchema(ctx context.Context, conn driver.Conn) error {
	if err := conn.Exec(ctx, RawEventsDDL()); err != nil {
		return fmt.Errorf("clickhouse: create %s: %w", tableName, err)
	}
	return nil
}
