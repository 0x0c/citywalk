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
