package model_test

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/event/model"
)

func validCustomEvent() model.Event {
	return model.Event{
		ID:         "01912d2c-1e2f-7abc-8def-000000000001",
		ChannelID:  "channel-1",
		Kind:       model.KindCustom,
		Name:       "screen_view",
		DeviceTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestValidateAcceptsAValidCustomEvent(t *testing.T) {
	if err := validCustomEvent().Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsMissingID(t *testing.T) {
	e := validCustomEvent()
	e.ID = ""
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for missing ID, want an error")
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	e := validCustomEvent()
	e.Kind = model.Kind("not_a_real_kind")
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for an unknown kind, want an error")
	}
}

func TestValidateRejectsCustomEventWithNoName(t *testing.T) {
	e := validCustomEvent()
	e.Name = ""
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for a custom event with no name, want an error")
	}
}

func TestValidateRequiresMessageIDForImpressionFamily(t *testing.T) {
	e := validCustomEvent()
	e.Kind = model.KindImpression
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for an impression with no message_id, want an error")
	}
	e.MessageID = "message-1"
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for an impression with no variant_id, want an error")
	}
	e.VariantID = "variant-1"
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v for a fully populated impression, want nil", err)
	}
}

func TestValidateHoldoutQualifiedNeedsNoVariant(t *testing.T) {
	e := validCustomEvent()
	e.Kind = model.KindHoldoutQualified
	e.MessageID = "message-1"
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v for a holdout qualification with a message_id but no variant_id, want nil", err)
	}
}

func TestValidateSuppressionRequiresAKnownReason(t *testing.T) {
	e := validCustomEvent()
	e.Kind = model.KindSuppression
	e.MessageID = "message-1"
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for a suppression with no reason, want an error")
	}
	e.SuppressionReason = model.SuppressionReason("not_a_real_reason")
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil for a suppression with an unknown reason, want an error")
	}
	e.SuppressionReason = model.ReasonPerMessageCap
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v for a suppression with a known reason, want nil", err)
	}
}

func TestEventNameUsesKindForNonCustomEvents(t *testing.T) {
	e := validCustomEvent()
	e.Kind = model.KindImpression
	e.Name = "ignored"
	if got := e.EventName(); got != "impression" {
		t.Errorf("EventName() = %q, want %q", got, "impression")
	}
}

func TestEventNameUsesNameForCustomEvents(t *testing.T) {
	e := validCustomEvent()
	if got := e.EventName(); got != "screen_view" {
		t.Errorf("EventName() = %q, want %q", got, "screen_view")
	}
}

// TestClockSkewImplausibleFlagsOnlyOutsideTolerance covers Unit 1's clock-offset sanity bound: the
// ordinary cases (online, and a plausible offline backlog) must not be flagged, and both directions of
// an implausible divergence (a clock reading into the future, and a clock implausibly far in the past)
// must be.
func TestClockSkewImplausibleFlagsOnlyOutsideTolerance(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		deviceTime time.Time
		want       bool
	}{
		"online, device time equals server time":               {serverTime, false},
		"ordinary small delay, device time before server time": {serverTime.Add(-time.Minute), false},
		"a plausible offline backlog, well under MaxPastSkew":  {serverTime.Add(-7 * 24 * time.Hour), false},
		"at the exact past boundary is still plausible":        {serverTime.Add(-model.MaxPastSkew), false},
		"just past the past boundary is implausible":           {serverTime.Add(-model.MaxPastSkew - time.Second), true},
		"a small, unremarkable amount of clock drift ahead":    {serverTime.Add(time.Minute), false},
		"at the exact future boundary is still plausible":      {serverTime.Add(model.MaxFutureSkew), false},
		"just past the future boundary is implausible":         {serverTime.Add(model.MaxFutureSkew + time.Second), true},
		"a clock reading days into the future is implausible":  {serverTime.Add(72 * time.Hour), true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := model.ClockSkewImplausible(tt.deviceTime, serverTime); got != tt.want {
				t.Errorf("ClockSkewImplausible(%v, %v) = %v, want %v", tt.deviceTime, serverTime, got, tt.want)
			}
		})
	}
}

// TestEffectiveTimeReturnsDeviceTimeWithinTolerance is the "not trusted blindly, but not discarded
// either" half of the two-timestamp rule: an event within tolerance still buckets by its own device
// time, which is what lets a late-but-plausible arrival land in the day it actually belongs to.
func TestEffectiveTimeReturnsDeviceTimeWithinTolerance(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	tests := map[string]time.Time{
		"before server time":             serverTime.Add(-time.Hour),
		"equal server time":              serverTime,
		"a plausible offline backlog":    serverTime.Add(-7 * 24 * time.Hour),
		"a small amount of future drift": serverTime.Add(time.Minute),
	}

	for name, deviceTime := range tests {
		t.Run(name, func(t *testing.T) {
			got := model.EffectiveTime(deviceTime, serverTime)
			if !got.Equal(deviceTime) {
				t.Errorf("EffectiveTime(%v, %v) = %v, want %v (device time)", deviceTime, serverTime, got, deviceTime)
			}
		})
	}
}

// TestEffectiveTimeClampsImplausibleDeviceTimeToServerTime is Unit 1's clamp rule for both directions
// of an implausible offset: a device clock that reads into the future, or one implausibly far in the
// past, must never be trusted for anything ordering-sensitive — the server's own receipt time takes
// over instead.
func TestEffectiveTimeClampsImplausibleDeviceTimeToServerTime(t *testing.T) {
	serverTime := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	tests := map[string]time.Time{
		"far in the future": serverTime.Add(72 * time.Hour),
		"far in the past":   serverTime.Add(-365 * 24 * time.Hour),
	}

	for name, deviceTime := range tests {
		t.Run(name, func(t *testing.T) {
			got := model.EffectiveTime(deviceTime, serverTime)
			if !got.Equal(serverTime) {
				t.Errorf("EffectiveTime(%v, %v) = %v, want %v (clamped to server time)", deviceTime, serverTime, got, serverTime)
			}
		})
	}
}
