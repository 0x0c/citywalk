// Package ordinal implements CW-0005 Unit 1: a dense integer ordinal per channel, the value both the
// forward and reverse bitmap indexes address by. An ordinal is allocated once, on a channel's first
// registration, and never reused — retiring a channel marks its row rather than deleting it, so a
// later channel can never inherit a retired ordinal and resurrect a deleted channel's membership.
package ordinal

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrRetired is returned, wrapped, by Lookup for a channel whose ordinal has been retired.
var ErrRetired = errors.New("ordinal: channel is retired")

// Allocate returns channelID's ordinal, assigning one if this is the channel's first registration.
// Concurrent calls for the same channel are safe: the second caller observes the first's row rather
// than allocating a duplicate.
func Allocate(ctx context.Context, pool *pgxpool.Pool, channelID string) (int64, error) {
	var ord int64
	err := pool.QueryRow(ctx, `
		INSERT INTO channel_ordinals (channel_id) VALUES ($1)
		ON CONFLICT (channel_id) DO UPDATE SET channel_id = EXCLUDED.channel_id
		RETURNING ordinal
	`, channelID).Scan(&ord)
	if err != nil {
		return 0, fmt.Errorf("ordinal: allocate for channel %s: %w", channelID, err)
	}
	return ord, nil
}

// Retire marks channelID's ordinal as retired: excluded from every future batch and incremental
// membership computation, and never reassigned.
func Retire(ctx context.Context, pool *pgxpool.Pool, channelID string) error {
	tag, err := pool.Exec(ctx, `UPDATE channel_ordinals SET retired = true WHERE channel_id = $1`, channelID)
	if err != nil {
		return fmt.Errorf("ordinal: retire channel %s: %w", channelID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ordinal: retire channel %s: no ordinal allocated", channelID)
	}
	return nil
}

// Lookup returns channelID's ordinal, or ErrRetired if it has been retired.
func Lookup(ctx context.Context, pool *pgxpool.Pool, channelID string) (int64, error) {
	var ord int64
	var retired bool
	err := pool.QueryRow(ctx,
		`SELECT ordinal, retired FROM channel_ordinals WHERE channel_id = $1`, channelID,
	).Scan(&ord, &retired)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("ordinal: lookup channel %s: no ordinal allocated", channelID)
		}
		return 0, fmt.Errorf("ordinal: lookup channel %s: %w", channelID, err)
	}
	if retired {
		return ord, fmt.Errorf("ordinal: lookup channel %s: %w", channelID, ErrRetired)
	}
	return ord, nil
}
