// Package payload implements CW-0002 Units 1 through 3: assembling the payload a device receives
// from the definitions it is eligible for, per the boundary rule (audience data never crosses into
// the payload) and the payload contract (complete, self-expiring, size-capped). It also implements
// CW-0006 Unit 6's size ceiling and cohort-labeled truncation metric — CW-0002's payload contract
// and CW-0006's transport protocol share this one assembly path rather than each keeping its own.
package payload

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	deliverysync "github.com/0x0c/citywalk/internal/delivery/sync"
	"github.com/0x0c/citywalk/internal/membership/reverse"
)

// Entry is one eligible message, projected onto exactly the fields CW-0002 Unit 2 allows across the
// boundary: no audience predicate, no segment identifier, no attribute value, and no other user's
// data. Content is the same wire document CW-0003's Variant.MarshalContentColumn produces, so a
// device's schema_version handling (CW-0003 Unit 4) applies unchanged.
type Entry struct {
	MessageID         string
	Version           int
	VariantID         string
	SchemaVersion     model.SchemaVersion
	Priority          int
	Content           []byte
	Triggers          []model.Trigger
	DisplayConditions []model.DisplayCondition
	ControlPolicy     model.ControlPolicy
	ExpiresAt         time.Time
}

// Payload is the complete, self-expiring answer to "what is this channel eligible for right now" —
// CW-0002 Unit 2's contract.
type Payload struct {
	Entries    []Entry
	NextSyncAt time.Time
	// ProjectBudgetRemaining is CW-0007 Unit 5's issuance: the estimated remaining project-wide
	// impression budget as of this assembly, for the device to spend locally. nil means the caller
	// (deliver.Sync) has no budget configured for this synchronization — distinct from a real zero,
	// which means the budget is fully spent. Build itself never sets this; it is out of payload
	// assembly's own scope (CW-0002/CW-0006) and is filled in by whichever caller has a budget
	// configuration to apply.
	ProjectBudgetRemaining *int
}

// truncationCounter records CW-0010 Unit 10's named payload-truncation-count metric: a truncation
// that goes unrecorded looks exactly like a campaign nobody qualified for.
var truncationCounter = mustCounter()

func mustCounter() metric.Int64Counter {
	c, err := otel.Meter("citywalk/delivery/payload").Int64Counter(
		"citywalk.delivery.payload_truncation_count",
		metric.WithDescription("Count of payload entries dropped for exceeding the size ceiling"),
	)
	if err != nil {
		// Int64Counter only fails on a malformed instrument name, which is fixed at compile time —
		// a real failure here would mean this package itself is broken, not a runtime condition.
		panic(err)
	}
	return c
}

// Build assembles channelID's payload: every active, in-window message whose audience segment
// appears in the channel's reverse-index bitmap (CW-0005 Unit 3), truncated to sizeCeilingBytes in
// priority order, with the next synchronization time CW-0002 Unit 3 requires.
func Build(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client,
	channelID, language string, now time.Time, sizeCeilingBytes int, syncInterval time.Duration, syncJitterFraction float64,
) (Payload, error) {
	nextSync := deliverysync.NextSyncAt(now, syncInterval, syncJitterFraction)

	segmentBM, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		return Payload{}, fmt.Errorf("payload: read reverse index: %w", err)
	}
	if segmentBM.IsEmpty() {
		return Payload{NextSyncAt: nextSync}, nil
	}

	ordinals := make([]int64, 0, segmentBM.GetCardinality())
	it := segmentBM.Iterator()
	for it.HasNext() {
		ordinals = append(ordinals, int64(it.Next()))
	}

	segmentIDs, err := segmentIDsForOrdinals(ctx, pool, ordinals)
	if err != nil {
		return Payload{}, err
	}
	if len(segmentIDs) == 0 {
		return Payload{NextSyncAt: nextSync}, nil
	}

	messageIDs, err := eligibleMessageIDs(ctx, pool, segmentIDs, now)
	if err != nil {
		return Payload{}, err
	}

	var entries []Entry
	for _, id := range messageIDs {
		msg, err := store.GetMessage(ctx, pool, id)
		if err != nil {
			return Payload{}, fmt.Errorf("payload: load message %s: %w", id, err)
		}
		entry, err := buildEntry(msg, language)
		if err != nil {
			return Payload{}, fmt.Errorf("payload: build entry for message %s: %w", id, err)
		}
		entries = append(entries, entry)
	}

	entries, truncated := truncate(entries, sizeCeilingBytes)
	if truncated > 0 {
		// Labeled by language — the cohort dimension Build actually has on hand — per CW-0006 Unit
		// 6: the metric must name the cohort, not just the count, so a project that has quietly
		// outgrown the ceiling for one locale doesn't hide behind an aggregate that looks fine.
		truncationCounter.Add(ctx, int64(truncated), metric.WithAttributes(
			attribute.String("citywalk.delivery.language", language),
		))
	}

	return Payload{Entries: entries, NextSyncAt: nextSync}, nil
}

// buildEntry projects msg onto the device-safe Entry shape, selecting msg's variant matching
// language — falling back to the first variant if none matches, since CW-0008's experiment
// assignment (the second half of CW-0003 Unit 1's "by language first and then by experiment
// assignment" selection rule) isn't built yet.
func buildEntry(msg model.Message, language string) (Entry, error) {
	if len(msg.Variants) == 0 {
		return Entry{}, fmt.Errorf("message has no variants")
	}
	variant := msg.Variants[0]
	for _, v := range msg.Variants {
		if v.Language == language {
			variant = v
			break
		}
	}
	content, err := variant.MarshalContentColumn()
	if err != nil {
		return Entry{}, fmt.Errorf("encode variant content: %w", err)
	}

	return Entry{
		MessageID:         msg.ID,
		Version:           msg.Version,
		VariantID:         variant.ID,
		SchemaVersion:     variant.SchemaVersion,
		Priority:          msg.Priority,
		Content:           content,
		Triggers:          msg.Triggers,
		DisplayConditions: msg.DisplayConditions,
		ControlPolicy:     msg.ControlPolicy,
		ExpiresAt:         msg.Window.End,
	}, nil
}

// truncate keeps entries (already priority-ordered by the eligibleMessageIDs query) while their
// cumulative Content size stays within sizeCeilingBytes, and reports how many were dropped —
// CW-0002 Unit 2's size ceiling, truncated in priority order rather than silently.
func truncate(entries []Entry, sizeCeilingBytes int) ([]Entry, int) {
	if sizeCeilingBytes <= 0 {
		return entries, 0
	}
	total := 0
	for i, e := range entries {
		total += len(e.Content)
		if total > sizeCeilingBytes {
			return entries[:i], len(entries) - i
		}
	}
	return entries, 0
}

func segmentIDsForOrdinals(ctx context.Context, pool *pgxpool.Pool, ordinals []int64) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM segments WHERE ordinal = ANY($1)`, ordinals)
	if err != nil {
		return nil, fmt.Errorf("payload: resolve segment ordinals: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("payload: scan segment id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payload: read segment ids: %w", err)
	}
	return ids, nil
}

// eligibleMessageIDs returns, in priority order (highest first), every active message whose window
// covers now and whose audience_ref names one of segmentIDs — CW-0002 Unit 1's boundary rule
// realized as a query: the predicate that decided eligibility already ran (CW-0005's batch or
// incremental maintenance), so this is a plain state and window filter, nothing more.
func eligibleMessageIDs(ctx context.Context, pool *pgxpool.Pool, segmentIDs []string, now time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT id FROM messages
		WHERE state = 'active' AND window_start <= $1 AND window_end > $1 AND audience_ref = ANY($2)
		ORDER BY priority DESC, id
	`, now, segmentIDs)
	if err != nil {
		return nil, fmt.Errorf("payload: query eligible messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("payload: scan eligible message: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payload: read eligible messages: %w", err)
	}
	return ids, nil
}
