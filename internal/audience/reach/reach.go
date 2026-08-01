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
	rows, err := pool.Query(ctx, `SELECT attributes FROM channels ORDER BY random() LIMIT $1`, sampleSize)
	if err != nil {
		return Estimate{}, fmt.Errorf("reach: sample channels: %w", err)
	}
	defer rows.Close()

	sampled := 0
	matches := 0
	for rows.Next() {
		var raw map[string]any
		if err := rows.Scan(&raw); err != nil {
			return Estimate{}, fmt.Errorf("reach: scan sampled channel: %w", err)
		}
		attrs, err := decodeAttributes(reg, raw)
		if err != nil {
			return Estimate{}, fmt.Errorf("reach: decode sampled channel attributes: %w", err)
		}
		matched, err := evaluator.Evaluate(p, attrs)
		if err != nil {
			return Estimate{}, fmt.Errorf("reach: evaluate sampled channel: %w", err)
		}
		sampled++
		if matched {
			matches++
		}
	}
	if err := rows.Err(); err != nil {
		return Estimate{}, fmt.Errorf("reach: read sampled channels: %w", err)
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
