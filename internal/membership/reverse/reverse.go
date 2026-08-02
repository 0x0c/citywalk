// Package reverse implements CW-0005 Unit 3: the per-channel bitmap of segment ordinals the delivery
// service reads on every payload assembly. It lives in Redis (CW-0010 Unit 4) as one key per
// channel — a single lookup returning under a hundred bytes, which is what makes the audience half
// of payload assembly effectively free.
package reverse

import (
	"context"
	"errors"
	"fmt"

	"github.com/RoaringBitmap/roaring"
	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces the reverse index within Redis, since CW-0010 Unit 4 shares the instance
// with CW-0006's entity-tag cache and CW-0007's counters.
const keyPrefix = "membership:reverse:"

func key(channelID string) string {
	return keyPrefix + channelID
}

// Get returns channelID's segment membership bitmap, or an empty bitmap if the channel has never
// been indexed (matches no segment).
func Get(ctx context.Context, client *redis.Client, channelID string) (*roaring.Bitmap, error) {
	data, err := client.Get(ctx, key(channelID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return roaring.New(), nil
		}
		return nil, fmt.Errorf("reverse: get channel %s: %w", channelID, err)
	}
	bm := roaring.New()
	if err := bm.UnmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("reverse: decode channel %s: %w", channelID, err)
	}
	return bm, nil
}

// Set overwrites channelID's segment membership bitmap.
func Set(ctx context.Context, client *redis.Client, channelID string, bm *roaring.Bitmap) error {
	data, err := bm.MarshalBinary()
	if err != nil {
		return fmt.Errorf("reverse: encode channel %s: %w", channelID, err)
	}
	if err := client.Set(ctx, key(channelID), data, 0).Err(); err != nil {
		return fmt.Errorf("reverse: set channel %s: %w", channelID, err)
	}
	return nil
}

// SetMany overwrites several channels' bitmaps in one Redis transaction (MULTI/EXEC), so a reader
// can never observe some of the batch applied and the rest not — the same torn-read prevention
// CW-0005 Unit 4 requires of the forward index's generation swap, given Redis's own tools rather
// than a generation number.
func SetMany(ctx context.Context, client *redis.Client, bitmaps map[string]*roaring.Bitmap) error {
	if len(bitmaps) == 0 {
		return nil
	}
	_, err := client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for channelID, bm := range bitmaps {
			data, err := bm.MarshalBinary()
			if err != nil {
				return fmt.Errorf("encode channel %s: %w", channelID, err)
			}
			pipe.Set(ctx, key(channelID), data, 0)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reverse: set %d channels: %w", len(bitmaps), err)
	}
	return nil
}
