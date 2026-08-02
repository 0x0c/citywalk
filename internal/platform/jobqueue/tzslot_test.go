package jobqueue

import (
	"testing"
	"time"
)

func TestSlotForOffset(t *testing.T) {
	cases := []struct {
		name   string
		offset time.Duration
		want   int
	}{
		{"UTC", 0, 0},
		{"first slot upper edge", 14 * time.Minute, 0},
		{"second slot lower edge", 15 * time.Minute, 1},
		{"Japan +09:00", 9 * time.Hour, 36},
		{"Nepal +05:45 (a :45 offset)", 5*time.Hour + 45*time.Minute, 23},
		{"Chatham +12:45 (a :45 offset)", 12*time.Hour + 45*time.Minute, 51},
		{"India +05:30 (a :30 offset)", 5*time.Hour + 30*time.Minute, 22},
		{"US Eastern -05:00, negative offset", -5 * time.Hour, 76},
		{"Marquesas -09:30, negative :30 offset", -9*time.Hour - 30*time.Minute, 58},
		{"last slot lower edge, 23:45", 23*time.Hour + 45*time.Minute, 95},
		{"last slot upper edge, 23:59", 23*time.Hour + 59*time.Minute, 95},
		{"exactly 24h wraps to slot 0", 24 * time.Hour, 0},
		{"-24h wraps to slot 0", -24 * time.Hour, 0},
		{"+14:00 and -10:00 land in the same slot (24h apart)", 14 * time.Hour, 56},
		{"-10:00 lands in the same slot as +14:00", -10 * time.Hour, 56},
		{"one full cycle plus a remainder", 25*time.Hour + 15*time.Minute, 5},
		{"negative offset just past a slot boundary", -15 * time.Minute, 95},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SlotForOffset(tc.offset); got != tc.want {
				t.Errorf("SlotForOffset(%s) = %d, want %d", tc.offset, got, tc.want)
			}
		})
	}
}

func TestSlotForOffsetAlwaysInRange(t *testing.T) {
	// Every offset in and well beyond the real-world range (-12:00 to +14:00) must land in [0, 96),
	// including multi-day-away values, since the bucketing is a pure modulo and must never panic or
	// return an out-of-range slot for any input.
	for m := -3 * 24 * 60; m <= 3*24*60; m += 5 {
		offset := time.Duration(m) * time.Minute
		slot := SlotForOffset(offset)
		if slot < 0 || slot >= SlotCount {
			t.Fatalf("SlotForOffset(%s) = %d, out of [0, %d)", offset, slot, SlotCount)
		}
	}
}

func TestOffsetForSlotRoundTrips(t *testing.T) {
	for slot := 0; slot < SlotCount; slot++ {
		offset := OffsetForSlot(slot)
		if offset < 0 || offset >= day {
			t.Fatalf("OffsetForSlot(%d) = %s, want a value in [0, 24h)", slot, offset)
		}
		if got := SlotForOffset(offset); got != slot {
			t.Errorf("SlotForOffset(OffsetForSlot(%d)) = %d, want %d", slot, got, slot)
		}
	}
}

func TestSlotForTimeOfDay(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
		want int
	}{
		{"midnight UTC", time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 0},
		{"09:00 UTC", time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC), 36},
		{"23:45 UTC", time.Date(2026, 8, 2, 23, 45, 0, 0, time.UTC), 95},
		{
			"a non-UTC location is normalized to UTC first",
			time.Date(2026, 8, 2, 18, 0, 0, 0, time.FixedZone("JST", 9*3600)), // 18:00+09:00 == 09:00 UTC
			36,
		},
		{"date does not affect the slot, only time of day", time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC), 36},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SlotForTimeOfDay(tc.t); got != tc.want {
				t.Errorf("SlotForTimeOfDay(%s) = %d, want %d", tc.t, got, tc.want)
			}
		})
	}
}

func TestActivationInstant(t *testing.T) {
	referenceDate := time.Date(2026, 8, 2, 12, 34, 56, 0, time.UTC) // time of day on referenceDate is irrelevant
	targetLocal := 9 * time.Hour                                    // "09:00 local"

	cases := []struct {
		name string
		slot int
		want time.Time
	}{
		{
			"UTC itself (slot 0) activates at 09:00 UTC on the reference date",
			0,
			time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
		},
		{
			"Japan +09:00 (slot 36) activates at 00:00 UTC on the reference date",
			36,
			time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			// -05:00's canonical [0, 24h) representative (per SlotForOffset's modulo rule) is +19:00,
			// not -05:00 itself, so subtracting it lands the instant a full calendar day earlier than
			// the "signed offset" intuition would suggest (Aug 1 14:00, not Aug 2 14:00) — the same
			// recurring UTC time-of-day either way, which is the property that actually matters for a
			// daily-recurring activation. See SlotForOffset's doc comment.
			"US Eastern -05:00 (slot 76) activates at 14:00 UTC, one day before the reference date",
			76,
			time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC),
		},
		{
			"Nepal +05:45 (slot 23) activates at 03:15 UTC on the reference date",
			23,
			time.Date(2026, 8, 2, 3, 15, 0, 0, time.UTC),
		},
		{
			"an offset near the day boundary rolls the activation onto the day before",
			// slot 56 represents +14:00 (Kiribati) under SlotForOffset's modulo rule; 09:00 local
			// there is 09:00 - 14:00 = -05:00, i.e. 19:00 UTC on the day before referenceDate.
			56,
			time.Date(2026, 8, 1, 19, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ActivationInstant(referenceDate, targetLocal, tc.slot)
			if !got.Equal(tc.want) {
				t.Errorf("ActivationInstant(%s, slot %d) = %s, want %s", targetLocal, tc.slot, got, tc.want)
			}
		})
	}
}

func TestActivationInstantEveryChannelInASlotActivatesWithinSlotDuration(t *testing.T) {
	// The property Unit 8's design leans on: enqueuing one activation per slot, rather than one per
	// channel, is only correct if every real offset in that slot reaches the target local time within
	// SlotDuration of the slot's computed activation instant. Check that directly for a spread of
	// offsets across the realistic range, including :15/:30/:45 remainders.
	referenceDate := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	targetLocal := 9 * time.Hour

	for m := -12 * 60; m <= 14*60; m += 5 {
		offset := time.Duration(m) * time.Minute
		slot := SlotForOffset(offset)

		slotActivation := ActivationInstant(referenceDate, targetLocal, slot)
		// The exact activation instant for this precise offset, computed the same way but with the
		// real offset rather than the slot's representative one.
		exact := referenceDate.Add(targetLocal).Add(-offset)

		// Both instants recur daily, so compare their time of day rather than the (possibly
		// different) calendar date each lands on.
		diff := slotActivation.Sub(exact) % day
		if diff < 0 {
			diff += day
		}
		if diff >= SlotDuration && diff <= day-SlotDuration {
			t.Fatalf("offset %s (slot %d): slot activation %s time-of-day differs from exact activation %s by %s, want < %s",
				offset, slot, slotActivation, exact, diff, SlotDuration)
		}
	}
}
