// Package reach implements CW-0004 Unit 6: reach estimation by evaluating a predicate row-wise
// against a uniform sample of channels and scaling, rather than running the (accurate but slow)
// set-wise evaluation the interactive authoring loop cannot wait for.
package reach

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
)

// confidenceZ is the z-score for a 95% confidence interval. CW-0004 Unit 6 requires reporting an
// interval, not which confidence level to report it at; 95% is the conventional default absent a
// stated requirement.
const confidenceZ = 1.96

// Estimate is the answer CW-0004 Unit 6 requires: a count together with its confidence interval and
// the sample size behind it, so an author reads "about 40,000, give or take 12,000" rather than a
// bare, falsely precise number.
type Estimate struct {
	PopulationSize int
	SampleSize     int
	Count          int
	Low            int
	High           int
}

// EstimateReach samples sampleSize channels uniformly, evaluates p against each with evaluator, and
// scales the observed match rate — with its Wilson score interval — to the full population.
func EstimateReach(
	ctx context.Context, pool *pgxpool.Pool, evaluator *eval.Evaluator, reg *registry.Registry,
	p *predicate.Predicate, sampleSize int,
) (Estimate, error) {
	if sampleSize <= 0 {
		return Estimate{}, fmt.Errorf("reach: sampleSize must be positive, got %d", sampleSize)
	}

	var population int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channels`).Scan(&population); err != nil {
		return Estimate{}, fmt.Errorf("reach: count population: %w", err)
	}
	if population == 0 {
		return Estimate{PopulationSize: 0, SampleSize: 0, Count: 0, Low: 0, High: 0}, nil
	}

	// ORDER BY random() is a full-table sort, adequate for the minimal channels table this pass
	// works against; it does not scale to CW-0010's ten-million-row estimate and should become
	// TABLESAMPLE once the real channel table exists at that size.
	rows, err := pool.Query(ctx, `SELECT id, attributes FROM channels ORDER BY random() LIMIT $1`, sampleSize)
	if err != nil {
		return Estimate{}, fmt.Errorf("reach: sample channels: %w", err)
	}

	var channelIDs []string
	attrsByChannel := make(map[string]map[string]any)
	for rows.Next() {
		var channelID string
		var raw map[string]any
		if err := rows.Scan(&channelID, &raw); err != nil {
			rows.Close()
			return Estimate{}, fmt.Errorf("reach: scan sampled channel: %w", err)
		}
		attrs, err := decodeAttributes(reg, raw)
		if err != nil {
			rows.Close()
			return Estimate{}, fmt.Errorf("reach: decode sampled channel attributes: %w", err)
		}
		channelIDs = append(channelIDs, channelID)
		attrsByChannel[channelID] = attrs
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Estimate{}, fmt.Errorf("reach: read sampled channels: %w", err)
	}

	// CW-0004 Unit 5: an event-aggregate-sourced attribute is never stored in channels.attributes, so
	// it is fetched separately — one batched query per attribute across every sampled channel, rather
	// than one query per channel — and merged in before evaluation.
	if err := mergeAggregateValues(ctx, pool, reg, channelIDs, attrsByChannel); err != nil {
		return Estimate{}, err
	}

	sampled := 0
	matches := 0
	for _, channelID := range channelIDs {
		matched, err := evaluator.Evaluate(p, attrsByChannel[channelID])
		if err != nil {
			return Estimate{}, fmt.Errorf("reach: evaluate sampled channel %s: %w", channelID, err)
		}
		sampled++
		if matched {
			matches++
		}
	}

	lowP, centerP, highP := wilsonInterval(matches, sampled, confidenceZ)
	return Estimate{
		PopulationSize: population,
		SampleSize:     sampled,
		Count:          int(math.Round(centerP * float64(population))),
		Low:            int(math.Round(lowP * float64(population))),
		High:           int(math.Round(highP * float64(population))),
	}, nil
}

// wilsonInterval computes the Wilson score interval for a proportion observed as matches out of n
// trials, at the given z-score. It is preferred over the normal approximation because it stays
// within [0, 1] and remains reasonable at small n — exactly the sample sizes an interactive reach
// estimate uses.
func wilsonInterval(matches, n int, z float64) (low, center, high float64) {
	if n == 0 {
		return 0, 0, 0
	}
	p := float64(matches) / float64(n)
	nf := float64(n)
	z2 := z * z

	denom := 1 + z2/nf
	centerAdj := (p + z2/(2*nf)) / denom
	halfWidth := (z / denom) * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))

	low = math.Max(0, centerAdj-halfWidth)
	high = math.Min(1, centerAdj+halfWidth)
	return low, centerAdj, high
}

// mergeAggregateValues fills in every event-aggregate-sourced attribute the registry declares, for
// every channel in channelIDs, by summing CW-0009's targeting_rollup over each attribute's trailing
// window — one query per attribute across the whole sample, so a sample of a thousand channels costs
// as many aggregate queries as the registry has event-aggregate attributes, not one per channel.
// Reach's own row-wise evaluator (CW-0004 Unit 3) otherwise has no way to answer a predicate like
// "opened the route screen at least three times in the last seven days", since that value was never
// written into channels.attributes at all.
func mergeAggregateValues(
	ctx context.Context, pool *pgxpool.Pool, reg *registry.Registry, channelIDs []string, attrsByChannel map[string]map[string]any,
) error {
	if len(channelIDs) == 0 {
		return nil
	}
	for _, def := range reg.Definitions() {
		if def.Source != registry.SourceEventAggregate {
			continue
		}
		// pgx binds a Go int as bigint by default, and Postgres has no date - bigint operator (only
		// date - integer), so the window-days placeholder needs an explicit cast.
		rows, err := pool.Query(ctx, `
			SELECT channel_id, sum(count) FROM targeting_rollup
			WHERE channel_id = ANY($1) AND event_name = $2 AND day > current_date - $3::int
			GROUP BY channel_id
		`, channelIDs, def.AggregateEventName, def.AggregateWindowDays)
		if err != nil {
			return fmt.Errorf("reach: query event aggregate %q: %w", def.Name, err)
		}

		sums := make(map[string]float64, len(channelIDs))
		for rows.Next() {
			var channelID string
			var sum int64
			if err := rows.Scan(&channelID, &sum); err != nil {
				rows.Close()
				return fmt.Errorf("reach: scan event aggregate %q: %w", def.Name, err)
			}
			sums[channelID] = float64(sum)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reach: read event aggregate %q: %w", def.Name, err)
		}

		// A channel absent from targeting_rollup entirely never emitted the event in the window,
		// which is a real zero, not a missing value — every sampled channel's attribute map gets an
		// entry regardless of whether it appeared in the grouped result above.
		for _, channelID := range channelIDs {
			attrsByChannel[channelID][def.Name] = sums[channelID]
		}
	}
	return nil
}

// decodeAttributes converts a channel's raw jsonb-decoded attribute map — where every JSON number
// is a float64 and every timestamp is still an RFC 3339 string — into the Go-typed map the CEL
// evaluator expects, per each attribute's registered type.
func decodeAttributes(reg *registry.Registry, raw map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(raw))
	for name, value := range raw {
		def, ok := reg.Lookup(name)
		if !ok {
			continue // Data the registry no longer describes; not this predicate's concern.
		}
		switch def.Type {
		case registry.TypeTimestamp:
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("reach: attribute %q is not a string, want an RFC 3339 timestamp", name)
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil, fmt.Errorf("reach: attribute %q is not RFC 3339: %w", name, err)
			}
			out[name] = t
		case registry.TypeStringSet:
			list, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("reach: attribute %q is not a list", name)
			}
			strs := make([]string, len(list))
			for i, elem := range list {
				s, ok := elem.(string)
				if !ok {
					return nil, fmt.Errorf("reach: attribute %q element %d is not a string", name, i)
				}
				strs[i] = s
			}
			out[name] = strs
		default:
			out[name] = value
		}
	}
	return out, nil
}
