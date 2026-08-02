// Package ratelimit implements CW-0009 Unit 2's per-channel rate limit: a ceiling on how many events
// one channel may submit per window. It protects the platform from one device, not from a user — a
// bug in a release can put an event in a render loop, and without a per-channel ceiling that single
// device saturates the ingestion path for everyone.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter enforces Limit events per Window, per channel, using a fixed-window counter in Redis.
type Limiter struct {
	Redis  *redis.Client
	Limit  int64
	Window time.Duration
}

// Allow increments channelID's counter for the window containing now by n and reports whether the
// channel stayed within Limit. The increment happens whether or not the batch is ultimately allowed,
// so a device cannot use a rejected retry to spend more than its share of the window.
func (l Limiter) Allow(ctx context.Context, channelID string, n int64, now time.Time) (bool, error) {
	key := l.key(channelID, now)

	count, err := l.Redis.IncrBy(ctx, key, n).Result()
	if err != nil {
		return false, fmt.Errorf("ratelimit: incrby: %w", err)
	}
	if count == n {
		// First increment to land in this window: arm its expiry so the key doesn't outlive it.
		if err := l.Redis.Expire(ctx, key, l.Window).Err(); err != nil {
			return false, fmt.Errorf("ratelimit: expire: %w", err)
		}
	}
	return count <= l.Limit, nil
}

// key is the Redis key holding channelID's counter for the fixed window containing now. Both halves
// matter: the "citywalk:eventrate:" prefix keeps the counter clear of CW-0005's reverse index and
// CW-0007's budget counters in the Redis instance CW-0010 Unit 4 shares between them, and the window
// index is what makes the ceiling per-window rather than per-lifetime — a new window is a new key,
// so the old one simply expires rather than being reset by anyone.
func (l Limiter) key(channelID string, now time.Time) string {
	windowIndex := now.Unix() / int64(l.Window.Seconds())
	return fmt.Sprintf("citywalk:eventrate:%s:%d", channelID, windowIndex)
}
