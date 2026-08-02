package jobqueue

import "time"

// SlotCount is CW-0010 Unit 8's 96: one 15-minute bucket for every offset a channel's time zone can
// carry, including the ones 45 minutes past the hour (Nepal's +05:45, Chatham's +12:45). 96 buckets
// of 15 minutes span exactly one 24-hour cycle.
const SlotCount = 96

// SlotDuration is the width of one slot.
const SlotDuration = 24 * time.Hour / SlotCount

// day is the length of one full cycle slots wrap around: an offset and the same offset plus or minus
// 24 hours name the same slot, which is correct rather than a rounding error — see SlotForOffset's
// doc comment.
const day = 24 * time.Hour

// SlotForOffset returns which of the 96 slots offset falls into. offset is a UTC offset (for
// example, nine hours for Japan Standard Time, minus five hours for US Eastern Standard Time) and
// may be negative, positive, larger than 24 hours, or carry a 15-, 30-, or 45-minute remainder.
//
// The bucketing is offset modulo 24 hours, not offset clamped to a fixed range. That is deliberate,
// not an oversight: two offsets exactly 24 hours apart (say, +14:00 and -10:00, both real time zones)
// reach any given local wall-clock time — "09:00 local" — at the same recurring point in a 24-hour
// UTC cycle, one calendar day apart. A campaign activation scheduled by time of day is a daily
// recurrence, so those two offsets sharing a slot is exactly the behavior that makes "one activation
// per slot" correct: every offset in the slot reaches the target local time within the slot's 15
// minutes, once a day, forever, regardless of which calendar date it lands on for that particular
// channel.
func SlotForOffset(offset time.Duration) int {
	normalized := offset % day
	if normalized < 0 {
		normalized += day
	}
	return int(normalized / SlotDuration)
}

// OffsetForSlot returns slot's representative offset, normalized to [0, 24h). It is SlotForOffset's
// inverse for any offset already in that range: SlotForOffset(OffsetForSlot(slot)) == slot. An offset
// outside [0, 24h) that maps to slot (per SlotForOffset's modulo-24h rule) is not reconstructed
// exactly — only its position within the 24-hour cycle is, which is all activation scheduling needs.
func OffsetForSlot(slot int) time.Duration {
	return time.Duration(slot) * SlotDuration
}

// SlotForTimeOfDay returns the slot t's wall-clock time of day (in UTC) falls into: the slot whose
// window t is currently inside, treating t's hour and minute the same way SlotForOffset treats an
// offset duration since midnight. A periodic job ticking once per SlotDuration can call this with the
// firing time to learn which of the 96 slots just elapsed.
func SlotForTimeOfDay(t time.Time) int {
	u := t.UTC()
	sinceMidnight := time.Duration(u.Hour())*time.Hour +
		time.Duration(u.Minute())*time.Minute +
		time.Duration(u.Second())*time.Second
	return SlotForOffset(sinceMidnight)
}

// ActivationInstant returns the UTC instant at which a channel whose offset falls in slot reaches
// targetLocal (a duration since midnight — 9*time.Hour for "09:00") local wall-clock time, on
// referenceDate's UTC calendar date. It is CW-0010 Unit 8's "one activation per slot rather than one
// per channel": every channel in slot activates within SlotDuration of this instant, since every
// offset in the slot is within SlotDuration of the slot's representative offset.
//
// The returned instant can fall on referenceDate's UTC calendar date or the day before — expected,
// not a bug. OffsetForSlot always returns a representative in [0, 24h), so a slot standing in for a
// negative real-world offset (US Eastern's -05:00 normalizes to +19:00) computes an instant a full
// day earlier than the signed-offset intuition would suggest. Both name the same recurring UTC
// time-of-day, one repetition apart, which is what a daily-recurring activation actually needs; see
// SlotForOffset's doc comment for why the modulo bucketing is correct in the first place.
func ActivationInstant(referenceDate time.Time, targetLocal time.Duration, slot int) time.Time {
	midnight := time.Date(
		referenceDate.Year(), referenceDate.Month(), referenceDate.Day(),
		0, 0, 0, 0, time.UTC,
	)
	return midnight.Add(targetLocal).Add(-OffsetForSlot(slot))
}
