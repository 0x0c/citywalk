package assign

import "fmt"

// VariantRange is one entry in a RangeTable: the half-open bucket interval [Start, End) assigned to
// Variant, or to the holdout when IsHoldout is true (Variant is then empty).
type VariantRange struct {
	Variant   string
	Start     int
	End       int
	IsHoldout bool
}

// RangeTable maps bucket ranges to variants (CW-0008 Unit 3): assignment is a lookup against this
// table, never arithmetic over weights at assignment time. Ranges are ordered and must exactly
// partition [0, BucketCount).
type RangeTable struct {
	Ranges []VariantRange
}

// NewRangeTable builds the initial table for an experiment: order names each variant once, weights
// gives each variant's bucket width, and holdoutWeight reserves a trailing range for the experiment's
// holdout (CW-0008 Unit 4). The weights, plus holdoutWeight, must sum to exactly BucketCount.
func NewRangeTable(order []string, weights map[string]int, holdoutWeight int) (RangeTable, error) {
	if len(order) == 0 {
		return RangeTable{}, fmt.Errorf("assign: range table needs at least one variant")
	}
	ranges := make([]VariantRange, 0, len(order)+1)
	cumulative := 0
	seen := make(map[string]bool, len(order))
	for _, variant := range order {
		if seen[variant] {
			return RangeTable{}, fmt.Errorf("assign: duplicate variant %q in order", variant)
		}
		seen[variant] = true
		width, ok := weights[variant]
		if !ok {
			return RangeTable{}, fmt.Errorf("assign: no weight given for variant %q", variant)
		}
		if width < 0 {
			return RangeTable{}, fmt.Errorf("assign: variant %q has a negative weight %d", variant, width)
		}
		ranges = append(ranges, VariantRange{Variant: variant, Start: cumulative, End: cumulative + width})
		cumulative += width
	}
	if holdoutWeight < 0 {
		return RangeTable{}, fmt.Errorf("assign: holdout weight %d is negative", holdoutWeight)
	}
	ranges = append(ranges, VariantRange{Start: cumulative, End: cumulative + holdoutWeight, IsHoldout: true})
	cumulative += holdoutWeight

	if cumulative != BucketCount {
		return RangeTable{}, fmt.Errorf("assign: weights and holdout sum to %d, want %d", cumulative, BucketCount)
	}
	return RangeTable{Ranges: ranges}, nil
}

// Reweight computes the range table for new proportions over the same variants current already
// names, in the same order, moving only the buckets the new proportions require (CW-0008 Unit 3).
// Because each variant's range is a cumulative-sum boundary in a fixed order, a bucket that was
// within a variant's old range stays within its new range whenever the new range still covers it —
// only buckets past a shifted boundary move, which is the minimum any correct reweight can achieve.
//
// Reweight cannot add or remove a variant; that is a new experiment, not a reweight of an existing
// one, and current's variant set (order and names) must match newWeights exactly.
func Reweight(current RangeTable, newWeights map[string]int, newHoldoutWeight int) (RangeTable, error) {
	order := make([]string, 0, len(current.Ranges))
	for _, r := range current.Ranges {
		if r.IsHoldout {
			continue
		}
		order = append(order, r.Variant)
	}
	return NewRangeTable(order, newWeights, newHoldoutWeight)
}

// Lookup finds which range bucket falls in, and reports whether it landed in the holdout.
func (t RangeTable) Lookup(bucket int) (variant string, isHoldout bool, err error) {
	for _, r := range t.Ranges {
		if bucket >= r.Start && bucket < r.End {
			return r.Variant, r.IsHoldout, nil
		}
	}
	return "", false, fmt.Errorf("assign: bucket %d is not covered by any range in the table", bucket)
}
