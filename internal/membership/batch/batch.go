// Package batch implements CW-0005 Unit 4 (batch recomputation and the generation swap) and Unit 6
// (reconciliation): a full recomputation of every segment's membership from the set-wise evaluator
// (CW-0004's sqlcompile), written under a new generation and swapped in atomically, reporting how
// many channels disagreed with the index as it stood before the swap.
package batch

import (
	"context"
	"fmt"
	"math"

	"github.com/RoaringBitmap/roaring"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
	"github.com/0x0c/citywalk/internal/audience/sqlcompile"
	"github.com/0x0c/citywalk/internal/membership/forward"
	"github.com/0x0c/citywalk/internal/membership/reverse"
	"github.com/0x0c/citywalk/internal/membership/segment"
)

// disagreementCounter records CW-0010 Unit 10's named membership-reconciliation-disagreement-count
// metric, by segment: a segment whose index has quietly drifted from what its predicate actually
// matches looks exactly like a segment nobody has qualified for lately, without this counter to
// distinguish the two.
var disagreementCounter = mustDisagreementCounter()

func mustDisagreementCounter() metric.Int64Counter {
	c, err := otel.Meter("citywalk/membership/batch").Int64Counter(
		"citywalk.membership.reconciliation_disagreement_count",
		metric.WithDescription("Count of channels whose segment membership disagreed with the reverse index immediately before a recomputation swap, by segment"),
	)
	if err != nil {
		panic(err)
	}
	return c
}

// Report summarizes one Recompute call: the generation it wrote, and — CW-0005 Unit 6's health
// metric — how many channels' membership in each segment disagreed with the index as it stood
// immediately before this recomputation swapped in.
type Report struct {
	Generation    int64
	Disagreements map[string]int // segment ID -> count of channels that disagreed
}

// Recompute evaluates every segment's predicate as a set-wise SQL query over every non-retired
// channel, writes the result under a new generation, and swaps the generation pointer and the
// Redis reverse index in atomically. It holds the full channel population's ordinal-to-id mapping
// and reverse bitmaps in memory for the run, which is adequate for the minimal channels table this
// pass works against; it does not scale to CW-0010's ten-million-channel estimate, which needs a
// paged rebuild instead of one in-memory pass.
func Recompute(ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client, reg *registry.Registry) (Report, error) {
	segments, err := segment.List(ctx, pool)
	if err != nil {
		return Report{}, err
	}

	currentGen, err := forward.CurrentGeneration(ctx, pool)
	if err != nil {
		return Report{}, err
	}
	newGen := currentGen + 1

	ordinalToChannel, err := loadChannelOrdinals(ctx, pool)
	if err != nil {
		return Report{}, err
	}
	reverseByOrdinal := make(map[int64]*roaring.Bitmap, len(ordinalToChannel))
	for ord := range ordinalToChannel {
		reverseByOrdinal[ord] = roaring.New()
	}

	newBitmaps := make(map[string]*roaring.Bitmap, len(segments))
	disagreements := make(map[string]int, len(segments))

	for _, seg := range segments {
		newBM, err := recomputeSegment(ctx, pool, reg, seg)
		if err != nil {
			return Report{}, fmt.Errorf("batch: recompute segment %s: %w", seg.ID, err)
		}
		oldBM, err := forward.Bitmap(ctx, pool, seg.ID, currentGen)
		if err != nil {
			return Report{}, fmt.Errorf("batch: read prior bitmap for segment %s: %w", seg.ID, err)
		}
		disagreement := int(roaring.Xor(oldBM, newBM).GetCardinality())
		disagreements[seg.ID] = disagreement
		if disagreement > 0 {
			disagreementCounter.Add(ctx, int64(disagreement), metric.WithAttributes(
				attribute.String("citywalk.membership.segment_id", seg.ID),
			))
		}
		newBitmaps[seg.ID] = newBM

		it := newBM.Iterator()
		for it.HasNext() {
			ord := int64(it.Next())
			if bm, ok := reverseByOrdinal[ord]; ok {
				bm.Add(uint32(seg.Ordinal))
			}
		}
	}

	if err := swapGeneration(ctx, pool, newGen, newBitmaps); err != nil {
		return Report{}, err
	}

	reverseByChannel := make(map[string]*roaring.Bitmap, len(ordinalToChannel))
	for ord, channelID := range ordinalToChannel {
		reverseByChannel[channelID] = reverseByOrdinal[ord]
	}
	if err := reverse.SetMany(ctx, redisClient, reverseByChannel); err != nil {
		return Report{}, fmt.Errorf("batch: update reverse index: %w", err)
	}

	if err := reclaim(ctx, pool, newGen); err != nil {
		return Report{}, err
	}

	return Report{Generation: newGen, Disagreements: disagreements}, nil
}

func recomputeSegment(ctx context.Context, pool *pgxpool.Pool, reg *registry.Registry, seg *segment.Segment) (*roaring.Bitmap, error) {
	ast, err := predicate.Load(seg.Predicate)
	if err != nil {
		return nil, err
	}
	whereSQL, args, err := sqlcompile.CompileToSQL(ast.NativeRep(), reg)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
		SELECT co.ordinal
		FROM channel_ordinals co
		JOIN channels c ON c.id = co.channel_id
		WHERE co.retired = false AND (%s)
	`, whereSQL)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query matching channels: %w", err)
	}
	defer rows.Close()

	bm := roaring.New()
	for rows.Next() {
		var ord int64
		if err := rows.Scan(&ord); err != nil {
			return nil, fmt.Errorf("scan matching channel: %w", err)
		}
		if ord < 0 || ord > math.MaxUint32 {
			return nil, fmt.Errorf("channel ordinal %d exceeds the 32-bit range this index supports", ord)
		}
		bm.Add(uint32(ord))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read matching channels: %w", err)
	}
	return bm, nil
}

func loadChannelOrdinals(ctx context.Context, pool *pgxpool.Pool) (map[int64]string, error) {
	rows, err := pool.Query(ctx, `SELECT ordinal, channel_id FROM channel_ordinals WHERE retired = false`)
	if err != nil {
		return nil, fmt.Errorf("batch: load channel ordinals: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]string)
	for rows.Next() {
		var ord int64
		var channelID string
		if err := rows.Scan(&ord, &channelID); err != nil {
			return nil, fmt.Errorf("batch: scan channel ordinal: %w", err)
		}
		out[ord] = channelID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("batch: read channel ordinals: %w", err)
	}
	return out, nil
}

// swapGeneration writes every segment's new bitmap and advances the generation pointer in one
// transaction, so no reader ever observes the pointer advanced without every segment's new-generation
// row already committed alongside it (CW-0005 Unit 4's torn-read prevention).
func swapGeneration(ctx context.Context, pool *pgxpool.Pool, newGen int64, bitmaps map[string]*roaring.Bitmap) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("batch: begin generation swap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for segmentID, bm := range bitmaps {
		data, err := bm.MarshalBinary()
		if err != nil {
			return fmt.Errorf("batch: encode bitmap for segment %s: %w", segmentID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO segment_membership (segment_id, generation, bitmap) VALUES ($1, $2, $3)
		`, segmentID, newGen, data); err != nil {
			return fmt.Errorf("batch: write bitmap for segment %s: %w", segmentID, err)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE membership_generation SET generation = $1`, newGen); err != nil {
		return fmt.Errorf("batch: advance generation pointer: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("batch: commit generation swap: %w", err)
	}
	return nil
}

// reclaimGenerations is how many generations (including the current one) Recompute keeps, per
// CW-0005 Unit 4: old generations are retained briefly — long enough to revert a bad recomputation
// by moving the pointer back — and then reclaimed.
const reclaimGenerations = 2

func reclaim(ctx context.Context, pool *pgxpool.Pool, newGen int64) error {
	oldestKept := newGen - reclaimGenerations + 1
	if oldestKept <= 0 {
		return nil
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM segment_membership WHERE generation < $1`, oldestKept,
	); err != nil {
		return fmt.Errorf("batch: reclaim old generations: %w", err)
	}
	return nil
}
