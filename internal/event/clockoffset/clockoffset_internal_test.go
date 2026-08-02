package clockoffset

import (
	"testing"
	"time"
)

// TestMedianOffsetEstimatesAConsistentSkew is the ordinary case Unit 1's design text calls out: a
// device whose clock genuinely runs fast (or slow) by a steady amount on every event should have that
// amount recovered from its history.
func TestMedianOffsetEstimatesAConsistentSkew(t *testing.T) {
	deltas := []time.Duration{90 * time.Second, 90 * time.Second, 90 * time.Second, 90 * time.Second, 90 * time.Second}
	if got := medianOffset(deltas); got != 90*time.Second {
		t.Errorf("medianOffset(%v) = %v, want 90s", deltas, got)
	}
}

// TestMedianOffsetEstimatesZeroForAnUnskewedChannel confirms the common case (a device whose clock is
// simply correct, modulo the small, unremarkable jitter every submission has) reports no correction
// rather than one accumulating from noise.
func TestMedianOffsetEstimatesZeroForAnUnskewedChannel(t *testing.T) {
	deltas := []time.Duration{-2 * time.Second, -1 * time.Second, 0, 1 * time.Second, 2 * time.Second}
	if got := medianOffset(deltas); got != 0 {
		t.Errorf("medianOffset(%v) = %v, want 0", deltas, got)
	}
}

// TestMedianOffsetResistsASingleImplausibleOutlier is the property the median is chosen for over a
// mean: one event with a wildly wrong device_time (a clock briefly reset to the epoch, a unit
// conversion bug on one submission) must not swing the whole channel's estimate toward it. A mean of
// this same slice would be dragged by well over half an hour toward the outlier; the median is
// unchanged, because one value out of ten can shift the sorted order by at most one rank, and both
// middle ranks here are still ordinary samples.
func TestMedianOffsetResistsASingleImplausibleOutlier(t *testing.T) {
	deltas := make([]time.Duration, 0, 10)
	for i := 0; i < 9; i++ {
		deltas = append(deltas, 90*time.Second)
	}
	deltas = append(deltas, 6*time.Hour) // one implausible outlier among nine consistent samples

	got := medianOffset(deltas)
	if got != 90*time.Second {
		t.Errorf("medianOffset(%v) = %v, want 90s (the outlier must not move the median)", deltas, got)
	}
}

// TestMedianOffsetHandlesAnEvenSampleSize covers the tie-break the odd-length case above does not
// exercise: an even number of samples averages the two middle values rather than picking one
// arbitrarily.
func TestMedianOffsetHandlesAnEvenSampleSize(t *testing.T) {
	deltas := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 40 * time.Second}
	if got := medianOffset(deltas); got != 25*time.Second {
		t.Errorf("medianOffset(%v) = %v, want 25s (average of the two middle values)", deltas, got)
	}
}

// TestMedianOffsetOfNoHistoryIsZero is Estimate's no-data case: a channel with nothing recorded yet
// gets no correction, which is what makes Correct a no-op until this channel has history to measure.
func TestMedianOffsetOfNoHistoryIsZero(t *testing.T) {
	if got := medianOffset(nil); got != 0 {
		t.Errorf("medianOffset(nil) = %v, want 0", got)
	}
}

// TestMedianOffsetDoesNotMutateItsInput guards the documented copy-before-sort behavior: a caller
// reusing its slice after calling medianOffset must still see its original order.
func TestMedianOffsetDoesNotMutateItsInput(t *testing.T) {
	deltas := []time.Duration{30 * time.Second, 10 * time.Second, 20 * time.Second}
	want := []time.Duration{30 * time.Second, 10 * time.Second, 20 * time.Second}

	medianOffset(deltas)

	for i := range deltas {
		if deltas[i] != want[i] {
			t.Errorf("medianOffset mutated its input: got %v, want %v", deltas, want)
			break
		}
	}
}

// TestCorrectSubtractsTheOffsetWhenWithinBounds is Correct's ordinary case: a device reporting 90s
// fast, corrected by a measured 90s offset, lands on the true time — well before server_time, so the
// clamp never engages.
func TestCorrectSubtractsTheOffsetWhenWithinBounds(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	deviceTime := serverTime.Add(-time.Minute + 90*time.Second) // "true" time is 1 minute before receipt

	got := Correct(deviceTime, serverTime, 90*time.Second)
	want := serverTime.Add(-time.Minute)
	if !got.Equal(want) {
		t.Errorf("Correct(%v, %v, 90s) = %v, want %v", deviceTime, serverTime, got, want)
	}
}

// TestCorrectClampsWhenTheCorrectedTimeWouldStillBeAfterServerTime is the composition the design text
// requires: correcting for skew does not exempt the result from the same receipt-time clamp
// consumer.effectiveTime applies to a raw device time. Here the estimated offset is smaller than the
// device's actual (undetected, e.g. newly changed) skew, so the corrected time would still land after
// server_time without the clamp.
func TestCorrectClampsWhenTheCorrectedTimeWouldStillBeAfterServerTime(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	deviceTime := serverTime.Add(time.Hour) // clock newly skewed far more than history has measured

	got := Correct(deviceTime, serverTime, 90*time.Second) // stale, too-small estimate
	if !got.Equal(serverTime) {
		t.Errorf("Correct(%v, %v, 90s) = %v, want %v (clamped to server time)", deviceTime, serverTime, got, serverTime)
	}
}

// TestCorrectWithZeroOffsetMatchesTheExistingClamp confirms Correct degrades to exactly
// consumer.effectiveTime's behavior when a channel has no measured offset yet (Estimate's no-history
// case) — the new function is a strict superset of the old clamp, not a divergent policy.
func TestCorrectWithZeroOffsetMatchesTheExistingClamp(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	before := serverTime.Add(-time.Hour)
	if got := Correct(before, serverTime, 0); !got.Equal(before) {
		t.Errorf("Correct(%v, %v, 0) = %v, want %v (device time, unclamped)", before, serverTime, got, before)
	}

	after := serverTime.Add(time.Hour)
	if got := Correct(after, serverTime, 0); !got.Equal(serverTime) {
		t.Errorf("Correct(%v, %v, 0) = %v, want %v (clamped)", after, serverTime, got, serverTime)
	}
}
