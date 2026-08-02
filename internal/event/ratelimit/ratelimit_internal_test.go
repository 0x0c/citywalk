package ratelimit

import (
	"strings"
	"testing"
	"time"
)

// TestKeyNamespacesEveryChannelUnderTheEventRatePrefix is the collision guard CW-0010 Unit 4's shared
// Redis instance needs: CW-0005's reverse index and CW-0007's budget counters live in the same
// keyspace, and one channel's counter must not be readable or writable as another's.
func TestKeyNamespacesEveryChannelUnderTheEventRatePrefix(t *testing.T) {
	l := Limiter{Limit: 100, Window: time.Minute}
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	got := l.key("channel-1", now)
	if !strings.HasPrefix(got, "citywalk:eventrate:") {
		t.Errorf("key = %q, want it prefixed with %q", got, "citywalk:eventrate:")
	}
	if other := l.key("channel-2", now); got == other {
		t.Errorf("key for channel-1 and channel-2 are both %q, want distinct keys", got)
	}
}

// TestKeyHoldsSteadyWithinAWindowAndAdvancesAcrossIt is what makes this a per-window ceiling rather
// than a per-lifetime one. A key that changed mid-window would hand a device a fresh allowance
// part-way through — exactly the render loop CW-0009 Unit 2's ceiling exists to bound — and one that
// did not change at the boundary would eventually deny a well-behaved channel forever.
func TestKeyHoldsSteadyWithinAWindowAndAdvancesAcrossIt(t *testing.T) {
	const window = time.Minute
	l := Limiter{Limit: 100, Window: window}
	start := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	if got, want := l.key("channel-1", start.Add(59*time.Second)), l.key("channel-1", start); got != want {
		t.Errorf("key late in the window = %q, want the same key as at its start (%q)", got, want)
	}
	if got, same := l.key("channel-1", start.Add(window)), l.key("channel-1", start); got == same {
		t.Errorf("key one window later = %q, want a different key", got)
	}
}

// TestKeyIsDerivedFromTheWindowLengthNotAFixedInterval keeps the window index tied to the configured
// Window: two limiters differing only in window length must not share a bucket, or a deployment
// changing the window would inherit the previous one's counts.
func TestKeyIsDerivedFromTheWindowLengthNotAFixedInterval(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 30, 0, 0, time.UTC)

	perMinute := Limiter{Window: time.Minute}.key("channel-1", now)
	perHour := Limiter{Window: time.Hour}.key("channel-1", now)
	if perMinute == perHour {
		t.Errorf("a per-minute and a per-hour limiter both key %q, want distinct buckets", perMinute)
	}
}
