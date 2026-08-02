// Package clockoffset implements the remaining half of CW-0009 Unit 1's two-timestamp rule: "reports
// on device time corrected by the measured clock offset and clamped to the receipt time." Unit 1
// already stores both timestamps (internal/event/model) and clamps an implausible device time to
// server time for rollup bucketing (internal/event/consumer's effectiveTime/bucketTime). Neither of
// those adjusts a plausible-but-skewed device time — a channel whose clock genuinely runs 90 seconds
// fast on every event has that skew baked into every device_time it reports, and the clamp only ever
// fires on the rare event far enough off to look implausible. This package estimates that steady,
// per-channel skew from recent history and corrects a device time by it, for analysis-facing reads of
// device time — never for rollup bucketing, which stays receipt-time-based per Unit 1's existing rule
// and is out of scope for this change.
//
// "Channel" is this codebase's device: internal/event/model.Event.ChannelID's own doc comment states
// it identifies the device, and CW-0009's design has no finer-grained device concept than the channel
// a client authenticates as.
package clockoffset

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultSampleSize is how many of a channel's most recent events Estimate reads by default. Large
// enough that a single implausible outlier (a device time typo, a momentary clock jump) cannot move
// the median past its neighbors, small enough to stay a cheap, index-only query and to track a
// genuine change in a device's skew (e.g. after the device's clock resyncs) within a bounded number of
// events rather than being permanently diluted by history from before the resync.
const DefaultSampleSize = 50

// Estimate reads channelID's sampleSize most recent events (by device time, descending — the same
// order attribution.fetchExposures already scans this table in, so no new index is needed) and returns
// the median of (device_time - server_time) across them: a positive result means the device's clock
// runs fast, a negative one means it runs slow. A channel with no events yet, or fewer events than
// sampleSize, is estimated from whatever it has; a channel with none at all returns a zero offset,
// which makes Correct a no-op — the same behavior as today, until this channel has history to measure.
//
// The median, not the mean, is the estimator: a single wildly implausible device_time (the kind
// consumer.effectiveTime's clamp exists to bound at bucketing time) shifts a mean by its own full
// magnitude but moves a median by at most one sample's worth of rank, so one bad event cannot swing a
// channel's whole correction the way it could swing an average.
func Estimate(ctx context.Context, pool *pgxpool.Pool, channelID string, sampleSize int) (time.Duration, error) {
	rows, err := pool.Query(ctx,
		`SELECT device_time, server_time FROM events_log WHERE channel_id = $1 ORDER BY device_time DESC LIMIT $2`,
		channelID, sampleSize,
	)
	if err != nil {
		return 0, fmt.Errorf("clockoffset: query recent events for channel %s: %w", channelID, err)
	}
	defer rows.Close()

	var deltas []time.Duration
	for rows.Next() {
		var deviceTime, serverTime time.Time
		if err := rows.Scan(&deviceTime, &serverTime); err != nil {
			return 0, fmt.Errorf("clockoffset: scan event for channel %s: %w", channelID, err)
		}
		deltas = append(deltas, deviceTime.Sub(serverTime))
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("clockoffset: iterate events for channel %s: %w", channelID, err)
	}

	return medianOffset(deltas), nil
}

// medianOffset returns the median of deltas, or zero for an empty slice. It sorts a copy rather than
// deltas itself, since a caller that reused its slice after calling this would otherwise see it
// silently reordered.
func medianOffset(deltas []time.Duration) time.Duration {
	if len(deltas) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(deltas))
	copy(sorted, deltas)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// Correct returns deviceTime adjusted by offset (Estimate's output) and then clamped so the result
// never lands after serverTime — Unit 1's "corrected by the measured clock offset and clamped to the
// receipt time" applied in that order. The two steps are deliberately kept separate operations
// composed here rather than folded into one: the offset correction handles a channel's steady skew,
// and the clamp is the same implausible-timestamp backstop consumer.effectiveTime applies for
// bucketing, needed here too because a correction is only as trustworthy as the estimate it is built
// from, and a bad estimate (or a genuinely new, larger skew this channel's history has not caught up
// to yet) must not be able to push a corrected time past what the server has actually received.
func Correct(deviceTime, serverTime time.Time, offset time.Duration) time.Time {
	corrected := deviceTime.Add(-offset)
	if corrected.After(serverTime) {
		return serverTime
	}
	return corrected
}
