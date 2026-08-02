// Package deliver implements CW-0006 Unit 2: the conditional request that answers "nothing has
// changed" from a single cache lookup, so the overwhelming majority of synchronizations — the ones
// where nothing changed — cost almost nothing. It also implements Unit 3's delta mode: when delta
// mode is enabled and a device's cursor can still be honored, the wire payload is trimmed to what
// changed since that cursor instead of the whole assembled payload.
package deliver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/delivery/changelog"
	"github.com/0x0c/citywalk/internal/delivery/cursor"
	"github.com/0x0c/citywalk/internal/delivery/etag"
	"github.com/0x0c/citywalk/internal/delivery/payload"
	deliverysync "github.com/0x0c/citywalk/internal/delivery/sync"
	"github.com/0x0c/citywalk/internal/governance/budget"
	"github.com/0x0c/citywalk/internal/membership/reverse"
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
	// DeltaModeEnabled is CW-0006 Unit 3's per-project switch: off by default, matching the unit's
	// own text ("the server enables the mode per project once the measured payload size justifies
	// it"). No project entity exists yet in this schema (CW-0001 Unit 1's administrative surface
	// isn't built that far) — this field is a phase-one stand-in for that switch, the same role
	// ProjectBudgetCap above already plays for CW-0007's project-wide budget.
	DeltaModeEnabled bool
}

// Result is what a synchronization resolves to: either Unchanged (no Payload — the device already
// has the current one) or a fresh Payload with the ETag that now identifies it.
type Result struct {
	ETag       string
	Unchanged  bool
	Payload    *payload.Payload
	NextSyncAt time.Time
	// IsDelta is true when Payload.Entries carries only what changed since the request's cursor
	// (CW-0006 Unit 3) rather than the complete eligible set. False — including whenever delta mode
	// is disabled, or the client's cursor could not be honored — means Payload.Entries is the full
	// payload exactly as CW-0006 Unit 2 already behaves, with Tombstones always empty.
	IsDelta bool
	// Tombstones lists the identifiers of messages the device should remove locally. Only ever
	// non-empty when IsDelta is true.
	Tombstones []string
	// Cursor is the fresh version cursor the device should store and echo on its next
	// synchronization. Empty whenever delta mode is disabled, or on the Unchanged path — nothing
	// changed, so the device's existing cursor (like its existing ETag) is still current and does
	// not need reissuing.
	Cursor string
}

const tagKeyPrefix = "delivery:etag:"

func tagKey(channelID string) string {
	return tagKeyPrefix + channelID
}

// Sync answers channelID's synchronization request. When clientETag matches the cached tag, it
// returns Unchanged without assembling anything — the fast path CW-0006 Unit 2's 50-millisecond
// budget depends on. Otherwise it assembles the payload (CW-0002's Build), computes the fresh tag
// (CW-0006 Unit 1), refreshes the cache, and returns the payload unless the freshly computed tag
// happens to still match what the device already holds. When cfg.DeltaModeEnabled and clientCursor
// can still be honored (CW-0006 Unit 3), the returned Payload carries only what changed since that
// cursor, plus tombstones, instead of the full assembled payload.
func Sync(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client,
	channelID, language, clientETag, clientCursor string, now time.Time, cfg Config,
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

	result := Result{ETag: tag, Payload: &p, NextSyncAt: p.NextSyncAt}

	if cfg.DeltaModeEnabled {
		deltaEntries, tombstones, freshCursor, ok, err := computeDelta(ctx, pool, redisClient, channelID, p, clientCursor, now)
		if err != nil {
			return Result{}, fmt.Errorf("deliver: compute delta: %w", err)
		}
		result.Cursor = freshCursor
		if ok {
			deltaPayload := payload.Payload{
				Entries: deltaEntries, NextSyncAt: p.NextSyncAt, ProjectBudgetRemaining: p.ProjectBudgetRemaining,
			}
			result.Payload = &deltaPayload
			result.IsDelta = true
			result.Tombstones = tombstones
		}
	}

	return result, nil
}

// computeDelta is CW-0006 Unit 3's core decision: given the full, freshly assembled payload p —
// delta mode only changes what goes on the wire, not what gets computed, which is Unit 4's job, so
// this never skips or duplicates payload.Build's eligibility logic — and the cursor a device sent
// back, decide whether a delta can be served. ok is false whenever the cursor cannot be honored for
// any reason (undecodable, a membership change the change log cannot see, or older than the log's
// retention), in which case the caller must send p.Entries whole; that is never an error, per Unit
// 3's own stated contract that an unhonorable cursor falls back to a full payload. freshCursor is
// always returned regardless of ok, since a fallback response still needs a cursor for the device's
// next attempt.
func computeDelta(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client,
	channelID string, p payload.Payload, clientCursor string, now time.Time,
) (entries []payload.Entry, tombstones []string, freshCursor string, ok bool, err error) {
	membershipBM, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("read membership: %w", err)
	}
	membershipHash, err := reverse.Hash(membershipBM)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("hash membership: %w", err)
	}
	currentSeq, err := changelog.CurrentSeq(ctx, pool)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("current change log seq: %w", err)
	}
	freshCursor = cursor.Encode(cursor.Cursor{Seq: currentSeq, MembershipHash: membershipHash, IssuedAt: now})

	clientCur, decoded := cursor.Decode(clientCursor)
	honorable := decoded &&
		clientCur.MembershipHash == membershipHash &&
		clientCur.Seq <= currentSeq &&
		!clientCur.IssuedAt.Before(now.Add(-changelog.Retention))
	if !honorable {
		return nil, nil, freshCursor, false, nil
	}

	changes, err := changelog.Since(ctx, pool, clientCur.Seq)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("read change log since %d: %w", clientCur.Seq, err)
	}
	// Ascending seq order (changelog.Since's own contract) means the last write for a given message
	// id in this loop is its latest, which is all upsert-vs-tombstone bookkeeping this function does
	// with Kind — see computeDelta's own doc comment on why that's enough.
	latestKind := make(map[string]changelog.Kind, len(changes))
	for _, c := range changes {
		latestKind[c.MessageID] = c.Kind
	}

	windowStarts, err := windowStartsFor(ctx, pool, entryMessageIDs(p.Entries))
	if err != nil {
		return nil, nil, "", false, err
	}

	inNow := make(map[string]bool, len(p.Entries))
	for _, e := range p.Entries {
		inNow[e.MessageID] = true
		_, changed := latestKind[e.MessageID]
		// A message's window opening is never itself a change log event (nothing runs on a timer to
		// notice it) — the only other way a message already unaffected by the change log can become
		// newly eligible for a channel whose own membership hash hasn't moved either.
		newlyInWindow := !windowStarts[e.MessageID].Before(clientCur.IssuedAt)
		if changed || newlyInWindow {
			entries = append(entries, e)
		}
	}

	for messageID, kind := range latestKind {
		// A window closing needs no tombstone: payload.Entry.ExpiresAt already carries the message's
		// window end, and the device's own self-expiry (CW-0002's payload contract) removes it
		// without a server round trip. Only a message that left this channel's eligible set for a
		// reason self-expiry cannot cover — a kill switch transition — needs one, and only when it
		// is not already back in the eligible set by a later event.
		if kind == changelog.KindTombstone && !inNow[messageID] {
			tombstones = append(tombstones, messageID)
		}
	}

	return entries, tombstones, freshCursor, true, nil
}

func windowStartsFor(ctx context.Context, pool *pgxpool.Pool, messageIDs []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(messageIDs))
	if len(messageIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT id, window_start FROM messages WHERE id = ANY($1)`, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("read window_start: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var start time.Time
		if err := rows.Scan(&id, &start); err != nil {
			return nil, fmt.Errorf("scan window_start: %w", err)
		}
		out[id] = start
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read window_start rows: %w", err)
	}
	return out, nil
}

func entryMessageIDs(entries []payload.Entry) []string {
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.MessageID
	}
	return ids
}
