// Package deliver implements CW-0006 Unit 2: the conditional request that answers "nothing has
// changed" from a single cache lookup, so the overwhelming majority of synchronizations — the ones
// where nothing changed — cost almost nothing.
package deliver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/delivery/etag"
	"github.com/0x0c/citywalk/internal/delivery/payload"
	deliverysync "github.com/0x0c/citywalk/internal/delivery/sync"
	"github.com/0x0c/citywalk/internal/governance/budget"
)

// Config holds the parameters Sync needs beyond the request itself.
type Config struct {
	SizeCeilingBytes   int
	SyncInterval       time.Duration
	SyncJitterFraction float64
	// TagCacheTTL is how long the per-channel entity tag CW-0006 Unit 2 requires stays cached — the
	// "short expiry" the unit names, inside which a repeat synchronization is a single Redis lookup
	// rather than a full assembly.
	TagCacheTTL time.Duration
	// ProjectBudgetCap and ProjectBudgetWindow configure CW-0007 Unit 5's budget issuance. A zero
	// ProjectBudgetCap disables issuance entirely — Payload.ProjectBudgetRemaining stays nil — which
	// is what every caller that predates CW-0007 gets by leaving these fields unset.
	ProjectBudgetCap    int
	ProjectBudgetWindow time.Duration
}

// Result is what a synchronization resolves to: either Unchanged (no Payload — the device already
// has the current one) or a fresh Payload with the ETag that now identifies it.
type Result struct {
	ETag       string
	Unchanged  bool
	Payload    *payload.Payload
	NextSyncAt time.Time
}

const tagKeyPrefix = "delivery:etag:"

func tagKey(channelID string) string {
	return tagKeyPrefix + channelID
}

// Sync answers channelID's synchronization request. When clientETag matches the cached tag, it
// returns Unchanged without assembling anything — the fast path CW-0006 Unit 2's 50-millisecond
// budget depends on. Otherwise it assembles the payload (CW-0002's Build), computes the fresh tag
// (CW-0006 Unit 1), refreshes the cache, and returns the payload unless the freshly computed tag
// happens to still match what the device already holds.
func Sync(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client,
	channelID, language, clientETag string, now time.Time, cfg Config,
) (Result, error) {
	if clientETag != "" {
		cached, err := redisClient.Get(ctx, tagKey(channelID)).Result()
		switch {
		case err == nil && cached == clientETag:
			nextSync := deliverysync.NextSyncAt(now, cfg.SyncInterval, cfg.SyncJitterFraction)
			return Result{ETag: cached, Unchanged: true, NextSyncAt: nextSync}, nil
		case err != nil && !errors.Is(err, redis.Nil):
			return Result{}, fmt.Errorf("deliver: read cached tag: %w", err)
		}
	}

	p, err := payload.Build(ctx, pool, redisClient, channelID, language, now, cfg.SizeCeilingBytes, cfg.SyncInterval, cfg.SyncJitterFraction)
	if err != nil {
		return Result{}, fmt.Errorf("deliver: build payload: %w", err)
	}
	tag := etag.Compute(p.Entries)
	if err := redisClient.Set(ctx, tagKey(channelID), tag, cfg.TagCacheTTL).Err(); err != nil {
		return Result{}, fmt.Errorf("deliver: cache tag: %w", err)
	}

	if clientETag != "" && tag == clientETag {
		return Result{ETag: tag, Unchanged: true, NextSyncAt: p.NextSyncAt}, nil
	}

	if cfg.ProjectBudgetCap > 0 {
		counter := budget.Counter{Redis: redisClient, Window: cfg.ProjectBudgetWindow}
		remaining, err := counter.Remaining(ctx, channelID, cfg.ProjectBudgetCap, now)
		if err != nil {
			return Result{}, fmt.Errorf("deliver: read project budget: %w", err)
		}
		p.ProjectBudgetRemaining = &remaining
	}

	return Result{ETag: tag, Payload: &p, NextSyncAt: p.NextSyncAt}, nil
}
