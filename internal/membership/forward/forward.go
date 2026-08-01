// Package forward implements CW-0005 Unit 2's read side: the authoring interface's view of a
// segment's membership as a Roaring bitmap of channel ordinals, read at the current generation
// (CW-0005 Unit 4). Cardinality and set algebra are the bitmap's own operations
// (GetCardinality, And, Or, AndNot) — this package's only job is fetching the bytes.
package forward

import (
	"context"
	"errors"
	"fmt"

	"github.com/RoaringBitmap/roaring"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CurrentGeneration returns the generation every reader should fetch segment_membership rows at.
func CurrentGeneration(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var generation int64
	if err := pool.QueryRow(ctx, `SELECT generation FROM membership_generation`).Scan(&generation); err != nil {
		return 0, fmt.Errorf("forward: read current generation: %w", err)
	}
	return generation, nil
}

// Bitmap returns segmentID's membership bitmap at generation, or an empty bitmap if that segment
// has no row at that generation (it has never been computed, or predates the segment's creation).
func Bitmap(ctx context.Context, pool *pgxpool.Pool, segmentID string, generation int64) (*roaring.Bitmap, error) {
	var data []byte
	err := pool.QueryRow(ctx,
		`SELECT bitmap FROM segment_membership WHERE segment_id = $1 AND generation = $2`,
		segmentID, generation,
	).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return roaring.New(), nil
		}
		return nil, fmt.Errorf("forward: read bitmap for segment %s generation %d: %w", segmentID, generation, err)
	}
	bm := roaring.New()
	if err := bm.UnmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("forward: decode bitmap for segment %s generation %d: %w", segmentID, generation, err)
	}
	return bm, nil
}

// CurrentBitmap returns segmentID's membership bitmap at the current generation.
func CurrentBitmap(ctx context.Context, pool *pgxpool.Pool, segmentID string) (*roaring.Bitmap, error) {
	generation, err := CurrentGeneration(ctx, pool)
	if err != nil {
		return nil, err
	}
	return Bitmap(ctx, pool, segmentID, generation)
}
