// Package report implements CW-0009 Unit 7 (the per-campaign suppression breakdown) and the read
// side of Unit 5's rollups (FR-RPT-02, FR-RPT-04): the figures a campaign owner reads — how many
// devices were eligible, how many were shown, how many clicked or dismissed, and how many were
// suppressed for each reason — assembled from campaign_rollup, suppression_rollup, and reach_sketch
// rather than computed by scanning raw events.
package report

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/hll"
)

// SuppressionBreakdown is messageID's suppression report: how many impressions were actually shown,
// alongside a count per suppression reason — the pairing Unit 7 exists to make possible.
type SuppressionBreakdown struct {
	MessageID   string
	Impressions int64
	ByReason    map[string]int64
}

// Suppressions builds messageID's suppression breakdown from suppression_rollup and the impression
// count already in campaign_rollup.
func Suppressions(ctx context.Context, pool *pgxpool.Pool, messageID string) (SuppressionBreakdown, error) {
	breakdown := SuppressionBreakdown{MessageID: messageID, ByReason: make(map[string]int64)}

	rows, err := pool.Query(ctx,
		`SELECT reason, sum(count) FROM suppression_rollup WHERE message_id = $1 GROUP BY reason`,
		messageID,
	)
	if err != nil {
		return SuppressionBreakdown{}, fmt.Errorf("report: query suppressions for message %s: %w", messageID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var count int64
		if err := rows.Scan(&reason, &count); err != nil {
			return SuppressionBreakdown{}, fmt.Errorf("report: scan suppression: %w", err)
		}
		breakdown.ByReason[reason] = count
	}
	if err := rows.Err(); err != nil {
		return SuppressionBreakdown{}, fmt.Errorf("report: iterate suppressions: %w", err)
	}

	var impressions int64
	err = pool.QueryRow(ctx,
		`SELECT COALESCE(sum(count), 0) FROM campaign_rollup WHERE message_id = $1 AND kind = 'impression'`,
		messageID,
	).Scan(&impressions)
	if err != nil {
		return SuppressionBreakdown{}, fmt.Errorf("report: query impressions for message %s: %w", messageID, err)
	}
	breakdown.Impressions = impressions

	return breakdown, nil
}

// VariantReport is FR-RPT-02's per-variant figures, plus the estimated unique reach FR-AUD-06's
// sibling requirement for delivered campaigns (not authored ones) asks for.
type VariantReport struct {
	VariantID      string
	Impressions    int64
	Clicks         int64
	Dismissals     int64
	EstimatedReach float64
}

// Variants builds messageID's per-variant report from campaign_rollup's exact counts and
// reach_sketch's estimate, in the order variants were first seen in campaign_rollup.
func Variants(ctx context.Context, pool *pgxpool.Pool, messageID string) ([]VariantReport, error) {
	rows, err := pool.Query(ctx,
		`SELECT variant_id, kind, sum(count) FROM campaign_rollup WHERE message_id = $1 GROUP BY variant_id, kind`,
		messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("report: query campaign rollup for message %s: %w", messageID, err)
	}
	defer rows.Close()

	byVariant := make(map[string]*VariantReport)
	var order []string
	for rows.Next() {
		var variantID, kind string
		var count int64
		if err := rows.Scan(&variantID, &kind, &count); err != nil {
			return nil, fmt.Errorf("report: scan campaign rollup: %w", err)
		}
		v, ok := byVariant[variantID]
		if !ok {
			v = &VariantReport{VariantID: variantID}
			byVariant[variantID] = v
			order = append(order, variantID)
		}
		switch kind {
		case "impression":
			v.Impressions = count
		case "button_press":
			v.Clicks = count
		case "dismissal":
			v.Dismissals = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("report: iterate campaign rollup: %w", err)
	}

	reports := make([]VariantReport, 0, len(order))
	for _, variantID := range order {
		v := byVariant[variantID]
		reach, err := estimateReach(ctx, pool, messageID, variantID)
		if err != nil {
			return nil, err
		}
		v.EstimatedReach = reach
		reports = append(reports, *v)
	}
	return reports, nil
}

// estimateReach merges every day's sketch on record for messageID and variantID and returns the
// merged estimate — a week's reach without rescanning a single raw event.
func estimateReach(ctx context.Context, pool *pgxpool.Pool, messageID, variantID string) (float64, error) {
	rows, err := pool.Query(ctx,
		`SELECT sketch FROM reach_sketch WHERE message_id = $1 AND variant_id = $2`,
		messageID, variantID,
	)
	if err != nil {
		return 0, fmt.Errorf("report: query reach sketches for %s/%s: %w", messageID, variantID, err)
	}
	defer rows.Close()

	merged := hll.New()
	found := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return 0, fmt.Errorf("report: scan reach sketch: %w", err)
		}
		s, err := hll.Unmarshal(raw)
		if err != nil {
			return 0, fmt.Errorf("report: unmarshal reach sketch: %w", err)
		}
		if err := merged.Merge(s); err != nil {
			return 0, fmt.Errorf("report: merge reach sketch: %w", err)
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("report: iterate reach sketches: %w", err)
	}
	if !found {
		return 0, nil
	}
	return merged.Estimate(), nil
}
