// Package clickhouse wires CW-0010 Unit 6's measurement store: a clickhouse-go connection and the
// events_raw schema CW-0009 Unit 4's merge-time deduplication needs (schema.go), plus the row shape
// and batch-insert operation internal/event/mirror writes through (row.go).
//
// CW-0010 Unit 11 keeps this whole package additive and off by default: nothing in this package is
// constructed unless an operator sets config.Config.ClickHouseMirrorEnabled and a DSN, which
// cmd/server/main.go checks before calling New. A phase-one deployment therefore never opens a
// ClickHouse connection at all — this codebase only needs to build and test against the second
// phase's design, not run against it yet.
package clickhouse

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// New opens a connection against dsn (e.g. "clickhouse://user:pass@host:9000/database"), verifies
// reachability with a Ping, and ensures events_raw exists (EnsureSchema) before returning — the same
// connect-verify shape internal/platform/postgres.NewPool and internal/platform/redisclient.New each
// already use for their own store, with schema creation folded in since there is no separate
// migration runner for ClickHouse (schema.go's EnsureSchema doc comment explains why one CREATE TABLE
// IF NOT EXISTS is enough for what this pass adds).
func New(ctx context.Context, dsn string) (driver.Conn, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: parse dsn: %w", err)
	}

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse: ping: %w", err)
	}
	if err := EnsureSchema(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse: ensure schema: %w", err)
	}
	return conn, nil
}

// Client wraps a ClickHouse connection with this package's own write operation, mirroring how
// internal/platform/postgres and internal/platform/redisclient each hand back a store's own driver
// type from their connect-and-verify constructor rather than an interface this codebase does not
// otherwise need.
type Client struct {
	Conn driver.Conn
}

// InsertEvents writes rows into events_raw in one batch (the driver's PrepareBatch/Send pattern).
// It is the only write path events_raw needs: every row is either genuinely new or a resend of one
// already present, and events_raw's ReplacingMergeTree engine (schema.go) resolves the difference at
// merge time, so this call never has to check first.
func (c *Client) InsertEvents(ctx context.Context, rows []RawEvent) error {
	if len(rows) == 0 {
		return nil
	}

	batch, err := c.Conn.PrepareBatch(ctx, fmt.Sprintf("INSERT INTO %s", tableName))
	if err != nil {
		return fmt.Errorf("clickhouse: prepare batch: %w", err)
	}
	defer func() { _ = batch.Close() }()

	for _, row := range rows {
		if err := batch.AppendStruct(&row); err != nil {
			return fmt.Errorf("clickhouse: append row %s: %w", row.ID, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("clickhouse: send batch: %w", err)
	}
	return nil
}
