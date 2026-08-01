// Package incremental implements CW-0005 Unit 5: when one channel's attributes change, re-evaluate
// only the segments whose predicates can be affected, for that one channel, and flip the
// corresponding bits in both indexes — rather than waiting for the next full batch recomputation.
package incremental

import (
	"context"
	"errors"
	"fmt"

	"github.com/RoaringBitmap/roaring"
	celast "github.com/google/cel-go/common/ast"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/audience/eval"
	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
	"github.com/0x0c/citywalk/internal/membership/forward"
	"github.com/0x0c/citywalk/internal/membership/ordinal"
	"github.com/0x0c/citywalk/internal/membership/reverse"
	"github.com/0x0c/citywalk/internal/membership/segment"
)

// UpdateChannel re-evaluates every incremental-eligible segment against channelID's current
// attributes and updates both indexes for that one channel. It is the caller's responsibility to
// call this only when an attribute the segment's dependency actually reads has changed — CW-0005
// Unit 5 describes a dependency map from attribute to segment for that purpose; this function does
// the re-evaluation and the bit flip once the caller has decided which segments to reconsider.
func UpdateChannel(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client, evaluator *eval.Evaluator,
	channelID string, segments []*segment.Segment, attributes map[string]any,
) error {
	ord, err := ordinal.Lookup(ctx, pool, channelID)
	if err != nil {
		return fmt.Errorf("incremental: %w", err)
	}
	if ord < 0 {
		return fmt.Errorf("incremental: channel %s has a negative ordinal", channelID)
	}
	ordU32 := uint32(ord)

	currentGen, err := forward.CurrentGeneration(ctx, pool)
	if err != nil {
		return err
	}

	reverseBM, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		return err
	}
	reverseChanged := false

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("incremental: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, seg := range segments {
		if seg.RefreshMode != segment.RefreshIncremental {
			continue // Batch-only segments (event-aggregate predicates) never take the incremental path.
		}

		matched, err := evaluator.Evaluate(seg.Predicate, attributes)
		if err != nil {
			return fmt.Errorf("incremental: evaluate segment %s for channel %s: %w", seg.ID, channelID, err)
		}

		if err := updateForwardBit(ctx, tx, seg.ID, currentGen, ordU32, matched); err != nil {
			return fmt.Errorf("incremental: update forward index for segment %s: %w", seg.ID, err)
		}

		segOrdU32 := uint32(seg.Ordinal)
		wasMember := reverseBM.Contains(segOrdU32)
		if matched && !wasMember {
			reverseBM.Add(segOrdU32)
			reverseChanged = true
		} else if !matched && wasMember {
			reverseBM.Remove(segOrdU32)
			reverseChanged = true
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("incremental: commit: %w", err)
	}

	if reverseChanged {
		if err := reverse.Set(ctx, redisClient, channelID, reverseBM); err != nil {
			return err
		}
	}
	return nil
}

// updateForwardBit locks segmentID's current-generation row, flips channelOrdinal's bit to match
// matched, and writes it back — a single-row update that needs none of Recompute's multi-segment
// generation swap, since it changes one fact for one channel rather than recomputing the world.
func updateForwardBit(ctx context.Context, tx pgx.Tx, segmentID string, generation int64, channelOrdinal uint32, matched bool) error {
	var data []byte
	err := tx.QueryRow(ctx, `
		SELECT bitmap FROM segment_membership WHERE segment_id = $1 AND generation = $2 FOR UPDATE
	`, segmentID, generation).Scan(&data)

	bm := roaring.New()
	switch {
	case err == nil:
		if err := bm.UnmarshalBinary(data); err != nil {
			return fmt.Errorf("decode bitmap: %w", err)
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No row yet at this generation (the segment has never been batch-computed); start empty.
	default:
		return fmt.Errorf("read bitmap: %w", err)
	}

	before := bm.Contains(channelOrdinal)
	if before == matched {
		return nil // Already correct; skip the write.
	}
	if matched {
		bm.Add(channelOrdinal)
	} else {
		bm.Remove(channelOrdinal)
	}

	newData, err := bm.MarshalBinary()
	if err != nil {
		return fmt.Errorf("encode bitmap: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO segment_membership (segment_id, generation, bitmap) VALUES ($1, $2, $3)
		ON CONFLICT (segment_id, generation) DO UPDATE SET bitmap = EXCLUDED.bitmap
	`, segmentID, generation, newData); err != nil {
		return fmt.Errorf("write bitmap: %w", err)
	}
	return nil
}

// DependencyMap returns, for each attribute in reg, the incremental-eligible segments whose
// predicate references it — CW-0005 Unit 5's dependency map, built by walking each segment's stored
// tree rather than maintained by hand. The caller re-evaluates only segments named for the attribute
// that actually changed, which is what bounds incremental work to the segments a change can affect.
func DependencyMap(segments []*segment.Segment, reg *registry.Registry) (map[string][]*segment.Segment, error) {
	deps := make(map[string][]*segment.Segment)
	for _, seg := range segments {
		if seg.RefreshMode != segment.RefreshIncremental {
			continue
		}
		names, err := referencedAttributes(seg)
		if err != nil {
			return nil, fmt.Errorf("incremental: dependency map for segment %s: %w", seg.ID, err)
		}
		for _, name := range names {
			if _, ok := reg.Lookup(name); ok {
				deps[name] = append(deps[name], seg)
			}
		}
	}
	return deps, nil
}

// referencedAttributes returns the distinct identifier names seg's predicate tree references.
func referencedAttributes(seg *segment.Segment) ([]string, error) {
	ast, err := predicate.Load(seg.Predicate)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var names []string
	visitor := celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.IdentKind {
			return
		}
		name := e.AsIdent()
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	})
	celast.PreOrderVisit(ast.NativeRep().Expr(), visitor)
	return names, nil
}
