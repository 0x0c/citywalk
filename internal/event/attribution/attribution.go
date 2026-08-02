// Package attribution implements CW-0009 Unit 6: attributing a conversion event to the variant (or
// the holdout) a channel was exposed to beforehand. The rule is stated explicitly, because every
// alternative reading of "did this campaign cause that event" is defensible and they disagree:
// attribution goes to the most recent qualifying exposure preceding the conversion, within the
// configured window; a channel in the holdout counts the same way a channel shown a variant does, so
// the holdout's rate is comparable to a variant's; and each physical conversion event attributes at
// most once, by its own identifier, so re-running Run over the same events is a no-op rather than a
// double count.
package attribution

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// HoldoutVariantID is the sentinel conversion_attributions uses in place of a real variant ID for a
// channel that converted after qualifying for messageID's holdout (CW-0008 Unit 4) rather than after
// an impression of a real variant.
const HoldoutVariantID = "__holdout__"

type exposure struct {
	ChannelID  string
	VariantID  string
	OccurredAt time.Time
}

type conversionEvent struct {
	ID         string
	ChannelID  string
	OccurredAt time.Time
}

// Run attributes every occurrence of conversionEventName within window of a preceding exposure to
// messageID (an impression or a holdout qualification), and records each attribution in
// conversion_attributions. It is safe to call repeatedly over a growing event log: an already
// recorded attribution is left alone (its primary key is the conversion event's own identifier), so
// events this call has already seen produce no new writes.
func Run(ctx context.Context, pool *pgxpool.Pool, messageID, conversionEventName string, window time.Duration) error {
	exposures, err := fetchExposures(ctx, pool, messageID)
	if err != nil {
		return err
	}
	conversions, err := fetchConversions(ctx, pool, conversionEventName)
	if err != nil {
		return err
	}

	byChannel := make(map[string][]exposure)
	for _, e := range exposures {
		byChannel[e.ChannelID] = append(byChannel[e.ChannelID], e)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("attribution: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, c := range conversions {
		best, ok := mostRecentExposureWithin(byChannel[c.ChannelID], c.OccurredAt, window)
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO conversion_attributions (message_id, channel_id, conversion_id, variant_id)
             VALUES ($1, $2, $3, $4)
             ON CONFLICT (message_id, channel_id, conversion_id) DO NOTHING`,
			messageID, c.ChannelID, c.ID, best.VariantID,
		); err != nil {
			return fmt.Errorf("attribution: record attribution for conversion %s: %w", c.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("attribution: commit: %w", err)
	}
	return nil
}

// mostRecentExposureWithin returns the exposure in exposures with the latest OccurredAt that is no
// later than convertedAt and no more than window before it — the "most recent impression preceding
// the conversion" rule.
func mostRecentExposureWithin(exposures []exposure, convertedAt time.Time, window time.Duration) (exposure, bool) {
	var best exposure
	found := false
	for _, ex := range exposures {
		if ex.OccurredAt.After(convertedAt) {
			continue
		}
		if convertedAt.Sub(ex.OccurredAt) > window {
			continue
		}
		if !found || ex.OccurredAt.After(best.OccurredAt) {
			best = ex
			found = true
		}
	}
	return best, found
}

// fetchExposures returns every impression and holdout qualification recorded against messageID, in
// per-channel time order. A holdout qualification carries no real variant, so it is reported under
// HoldoutVariantID — that substitution is what lets a holdout's conversion rate be computed by the
// same code path as a variant's.
func fetchExposures(ctx context.Context, pool *pgxpool.Pool, messageID string) ([]exposure, error) {
	rows, err := pool.Query(ctx,
		`SELECT channel_id, kind, COALESCE(variant_id::text, ''), device_time
         FROM events_log
         WHERE message_id = $1 AND kind IN ('impression', 'holdout_qualified')
         ORDER BY channel_id, device_time`,
		messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("attribution: query exposures for message %s: %w", messageID, err)
	}
	defer rows.Close()

	var exposures []exposure
	for rows.Next() {
		var channelID, kind, variantID string
		var occurredAt time.Time
		if err := rows.Scan(&channelID, &kind, &variantID, &occurredAt); err != nil {
			return nil, fmt.Errorf("attribution: scan exposure: %w", err)
		}
		if kind == "holdout_qualified" {
			variantID = HoldoutVariantID
		}
		exposures = append(exposures, exposure{ChannelID: channelID, VariantID: variantID, OccurredAt: occurredAt})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attribution: iterate exposures: %w", err)
	}
	return exposures, nil
}

func fetchConversions(ctx context.Context, pool *pgxpool.Pool, conversionEventName string) ([]conversionEvent, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text, channel_id, device_time
         FROM events_log
         WHERE kind = 'custom' AND name = $1
         ORDER BY channel_id, device_time`,
		conversionEventName,
	)
	if err != nil {
		return nil, fmt.Errorf("attribution: query conversions named %q: %w", conversionEventName, err)
	}
	defer rows.Close()

	var conversions []conversionEvent
	for rows.Next() {
		var c conversionEvent
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.OccurredAt); err != nil {
			return nil, fmt.Errorf("attribution: scan conversion: %w", err)
		}
		conversions = append(conversions, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attribution: iterate conversions: %w", err)
	}
	return conversions, nil
}

// Counts reads back the conversion counts Run has recorded for messageID, one entry per variant (and
// HoldoutVariantID for the holdout), per FR-RPT-04's "reports compare variants against each other and
// against the holdout."
func Counts(ctx context.Context, pool *pgxpool.Pool, messageID string) (map[string]int64, error) {
	rows, err := pool.Query(ctx,
		`SELECT variant_id, count(*) FROM conversion_attributions WHERE message_id = $1 GROUP BY variant_id`,
		messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("attribution: query counts for message %s: %w", messageID, err)
	}
	defer rows.Close()

	counts := make(map[string]int64)
	for rows.Next() {
		var variantID string
		var count int64
		if err := rows.Scan(&variantID, &count); err != nil {
			return nil, fmt.Errorf("attribution: scan count: %w", err)
		}
		counts[variantID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attribution: iterate counts: %w", err)
	}
	return counts, nil
}
