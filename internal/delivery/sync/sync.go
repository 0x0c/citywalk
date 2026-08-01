// Package sync implements CW-0002 Unit 3's freshness half: the server-dictated synchronization
// interval a payload carries, spread with jitter so a fixed interval does not turn every device that
// synchronized together into a thundering herd on the same cadence.
package sync

import (
	"math/rand/v2"
	"time"
)

// NextSyncAt returns the time a device should next synchronize: now plus baseInterval, plus a
// random offset uniformly distributed across [0, jitterFraction*baseInterval) — the spread CW-0002
// Unit 3 requires so a fifteen-minute interval does not fire for every device at once.
func NextSyncAt(now time.Time, baseInterval time.Duration, jitterFraction float64) time.Time {
	jitterRange := time.Duration(float64(baseInterval) * jitterFraction)
	var offset time.Duration
	if jitterRange > 0 {
		offset = time.Duration(rand.Int64N(int64(jitterRange)))
	}
	return now.Add(baseInterval).Add(offset)
}
