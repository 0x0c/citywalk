// Package changelog implements CW-0006 Unit 3's bounded change log: an append-only record of which
// messages had an eligibility-affecting event (a kill switch transition — see
// internal/platform/connectserver/admin.go's UpdateMessageState, the only mutation surface that
// exists today) and when, so a delta synchronization can ask "what changed since cursor" without
// re-deriving it from message_audit_log's broader per-transition history.
package changelog

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kind labels the direction of the transition that produced a row, for a human reading the table.
// The delta computation in internal/delivery/deliver does not trust Kind to decide upsert vs.
// tombstone — it cross-checks the changed message against the freshly assembled eligible set instead
// — so Kind only needs to be a reasonable label, not a precisely maintained one (see Record).
type Kind string

const (
	KindUpsert    Kind = "upsert"
	KindTombstone Kind = "tombstone"
)

// Retention is how long a change log row survives before Prune removes it. Seven days: long enough
// that a device idle for the better part of a week still gets a delta rather than falling back to a
// full resync, short enough that the table never grows past a few days' worth of edits even under a
// burst of them — and this item's own Motivation section puts campaign edits at "a few times a day",
// so a week's retention is at most a few dozen rows in practice.
const Retention = 7 * 24 * time.Hour

// Change is one row read back from the log.
type Change struct {
	Seq       int64
	MessageID string
	Kind      Kind
}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, so Record can run inside a caller's
// transaction (atomic with the state change that produced it) or stand alone.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Record appends one row for messageID and opportunistically prunes rows older than Retention in the
// same call, since this pass adds no background scheduler (CW-0010 Unit 11's single-process phase) —
// pruning on write is the cheapest way to keep the table bounded without one.
func Record(ctx context.Context, db querier, messageID string, kind Kind, occurredAt time.Time) error {
	if _, err := db.Exec(ctx,
		`INSERT INTO delivery_change_log (message_id, kind, occurred_at) VALUES ($1, $2, $3)`,
		messageID, string(kind), occurredAt,
	); err != nil {
		return fmt.Errorf("changelog: record %s for message %s: %w", kind, messageID, err)
	}
	if _, err := db.Exec(ctx,
		`DELETE FROM delivery_change_log WHERE occurred_at < $1`, occurredAt.Add(-Retention),
	); err != nil {
		return fmt.Errorf("changelog: prune: %w", err)
	}
	return nil
}

// CurrentSeq returns the highest seq the log currently holds, or 0 if it is empty — the seq CW-0006
// Unit 3's cursor embeds when freshly issued.
func CurrentSeq(ctx context.Context, db querier) (int64, error) {
	var seq *int64
	if err := db.QueryRow(ctx, `SELECT max(seq) FROM delivery_change_log`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("changelog: current seq: %w", err)
	}
	if seq == nil {
		return 0, nil
	}
	return *seq, nil
}

// Since returns every row with Seq greater than afterSeq, ordered by Seq ascending — every
// eligibility event a delta sync needs to consider since the device's cursor was issued.
func Since(ctx context.Context, db querier, afterSeq int64) ([]Change, error) {
	rows, err := db.Query(ctx,
		`SELECT seq, message_id, kind FROM delivery_change_log WHERE seq > $1 ORDER BY seq`, afterSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("changelog: since %d: %w", afterSeq, err)
	}
	defer rows.Close()

	var changes []Change
	for rows.Next() {
		var c Change
		var kind string
		if err := rows.Scan(&c.Seq, &c.MessageID, &kind); err != nil {
			return nil, fmt.Errorf("changelog: scan: %w", err)
		}
		c.Kind = Kind(kind)
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("changelog: read since %d: %w", afterSeq, err)
	}
	return changes, nil
}
