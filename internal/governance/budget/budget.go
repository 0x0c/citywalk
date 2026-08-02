// Package budget implements CW-0007 Unit 4's approximate sliding-window counter for the project-wide
// impression budget, and the atomic check-and-increment operation Unit 6's strict confirmation
// endpoint needs so that two servers handling two devices at once cannot both hand out the last
// remaining impression.
//
// The counter is two fixed buckets — the current window and the previous one — with the estimate
// being the current count plus the previous count weighted by the fraction of the window the
// previous bucket still covers. It is bounded in memory regardless of impression volume, cannot be
// gamed across a window boundary the way a naive fixed-window counter can, and its error is a known
// function of where in the window a request lands — the three properties CW-0007's *Detailed design*
// picks this scheme for over a fixed window, an exact sliding log, or a token bucket.
package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// script implements the whole read-rollover-check-increment-write sequence as one atomic operation
// (CW-0007 Unit 4: "updates through a single atomic script"), so a concurrent request from a second
// server can never observe the pre-increment state.
//
// KEYS[1] = the counter's hash key
// ARGV[1] = current window index (floor(now / windowSeconds))
// ARGV[2] = weight of the previous bucket (1 - elapsed fraction of the current window)
// ARGV[3] = increment to apply if allowed (0 for a read-only check, 1 for recording an impression)
// ARGV[4] = cap, or -1 to skip the cap check entirely (unconditional reconciliation)
// ARGV[5] = key TTL in seconds
//
// Returns {estimate_after_as_string, allowed_as_int}.
const script = `
local key = KEYS[1]
local windowIdx = tonumber(ARGV[1])
local weight = tonumber(ARGV[2])
local increment = tonumber(ARGV[3])
local cap = tonumber(ARGV[4])
local ttl = tonumber(ARGV[5])

local storedWindow = tonumber(redis.call('HGET', key, 'window'))
local count = tonumber(redis.call('HGET', key, 'count')) or 0
local prevCount = tonumber(redis.call('HGET', key, 'prev')) or 0

if storedWindow == nil then
  storedWindow = windowIdx
  count = 0
  prevCount = 0
elseif storedWindow < windowIdx then
  if storedWindow == windowIdx - 1 then
    prevCount = count
  else
    prevCount = 0
  end
  count = 0
  storedWindow = windowIdx
end

local estimateBefore = count + prevCount * weight

local allowed = 1
if cap >= 0 and (estimateBefore + increment) > cap then
  allowed = 0
  increment = 0
end

count = count + increment

redis.call('HSET', key, 'window', storedWindow, 'count', count, 'prev', prevCount)
redis.call('EXPIRE', key, ttl)

local estimateAfter = count + prevCount * weight
return {tostring(estimateAfter), allowed}
`

var evalScript = redis.NewScript(script)

// noCapCheck tells the script to skip the cap comparison and always allow the increment: used by
// Remaining (a read with a zero increment) and RecordImpression (an unconditional reconciliation —
// an impression that already happened is never itself subject to the cap).
const noCapCheck = -1

// Counter tracks one identity's rolling impression count against Window. identity is CW-0008-style: a
// user identifier where available, the channel identifier otherwise — this phase has no project
// entity of its own yet, so the counter is scoped per identity directly rather than per
// (project, identity), and a future project entity narrows the key without changing this package.
type Counter struct {
	Redis  *redis.Client
	Window time.Duration
}

func (c Counter) key(identity string) string {
	return fmt.Sprintf("citywalk:budget:%s", identity)
}

// windowParams computes the current window index and the previous bucket's weight for now, per
// CW-0007 Unit 4's formula.
func windowParams(now time.Time, window time.Duration) (windowIdx int64, weight float64) {
	seconds := int64(window.Seconds())
	nowUnix := now.Unix()
	windowIdx = nowUnix / seconds
	elapsedFraction := float64(nowUnix%seconds) / float64(seconds)
	return windowIdx, 1 - elapsedFraction
}

// Remaining reports identity's estimated remaining budget against cap as of now, without recording
// an impression — CW-0007 Unit 5's "the server issues, in each payload, the remaining project-wide
// budget... as of assembly."
func (c Counter) Remaining(ctx context.Context, identity string, cap int, now time.Time) (int, error) {
	estimate, _, err := c.eval(ctx, identity, now, 0, noCapCheck)
	if err != nil {
		return 0, err
	}
	remaining := float64(cap) - estimate
	if remaining < 0 {
		remaining = 0
	}
	// Rounded down: the payload must never overstate what's left, since the device spends against
	// this number directly (Unit 5) and an optimistic figure is what lets it exceed the cap.
	return int(remaining), nil
}

// RecordImpression unconditionally increments identity's counter by one — CW-0007 Unit 5's
// reconciliation from a reported impression. It is never itself capped: the impression it accounts
// for already happened, reported through CW-0009's event pipeline, so there is nothing left to deny.
func (c Counter) RecordImpression(ctx context.Context, identity string, now time.Time) error {
	_, _, err := c.eval(ctx, identity, now, 1, noCapCheck)
	return err
}

// CheckAndIncrement atomically reports whether recording one more impression for identity stays
// within cap and, if so, records it in the same operation — CW-0007 Unit 6's "decrements the
// authoritative counter and answers in one atomic operation, so two devices cannot both receive the
// last remaining impression."
func (c Counter) CheckAndIncrement(ctx context.Context, identity string, cap int, now time.Time) (bool, error) {
	_, allowed, err := c.eval(ctx, identity, now, 1, cap)
	return allowed, err
}

func (c Counter) eval(ctx context.Context, identity string, now time.Time, increment, cap int) (float64, bool, error) {
	windowIdx, weight := windowParams(now, c.Window)
	// Retained for slightly longer than one window so a request landing right at a boundary still
	// finds the previous bucket in place.
	ttlSeconds := int(c.Window.Seconds()) * 2

	res, err := evalScript.Run(ctx, c.Redis, []string{c.key(identity)}, windowIdx, weight, increment, cap, ttlSeconds).Result()
	if err != nil {
		return 0, false, fmt.Errorf("budget: eval: %w", err)
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) != 2 {
		return 0, false, fmt.Errorf("budget: unexpected script result %#v", res)
	}
	estimateStr, ok := arr[0].(string)
	if !ok {
		return 0, false, fmt.Errorf("budget: unexpected estimate type %#v", arr[0])
	}
	var estimate float64
	if _, err := fmt.Sscanf(estimateStr, "%f", &estimate); err != nil {
		return 0, false, fmt.Errorf("budget: parse estimate %q: %w", estimateStr, err)
	}
	allowedInt, ok := arr[1].(int64)
	if !ok {
		return 0, false, fmt.Errorf("budget: unexpected allowed type %#v", arr[1])
	}
	return estimate, allowedInt == 1, nil
}
