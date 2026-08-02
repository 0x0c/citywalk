package attribution

import (
	"testing"
	"time"
)

func at(minute int) time.Time {
	return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
}

func TestMostRecentExposureWithinPicksTheLatestPrecedingExposure(t *testing.T) {
	exposures := []exposure{
		{VariantID: "A", OccurredAt: at(0)},
		{VariantID: "B", OccurredAt: at(5)},
		{VariantID: "C", OccurredAt: at(20)}, // after the conversion; must be ignored
	}
	got, ok := mostRecentExposureWithin(exposures, at(10), time.Hour)
	if !ok || got.VariantID != "B" {
		t.Errorf("mostRecentExposureWithin() = (%+v, %v), want (variant B, true)", got, ok)
	}
}

func TestMostRecentExposureWithinRejectsOutsideTheWindow(t *testing.T) {
	exposures := []exposure{{VariantID: "A", OccurredAt: at(0)}}
	_, ok := mostRecentExposureWithin(exposures, at(120), time.Hour)
	if ok {
		t.Error("mostRecentExposureWithin() = ok for an exposure outside the window, want false")
	}
}

func TestMostRecentExposureWithinRejectsWhenNoExposurePrecedesTheConversion(t *testing.T) {
	exposures := []exposure{{VariantID: "A", OccurredAt: at(30)}}
	_, ok := mostRecentExposureWithin(exposures, at(10), time.Hour)
	if ok {
		t.Error("mostRecentExposureWithin() = ok when the only exposure is after the conversion, want false")
	}
}

func TestMostRecentExposureWithinReturnsFalseForNoExposures(t *testing.T) {
	if _, ok := mostRecentExposureWithin(nil, at(10), time.Hour); ok {
		t.Error("mostRecentExposureWithin() = ok for no exposures, want false")
	}
}
