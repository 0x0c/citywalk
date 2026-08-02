package model_test

import (
	"reflect"
	"testing"

	"github.com/0x0c/citywalk/internal/definition/model"
)

// TestActionRoundTripPreservesEveryMember walks all five members of CW-0003 Unit 2's tagged union
// through the wire shape and back. The discriminator is what makes the union decodable at all, so
// each member's own ActionKind is asserted on the decoded value rather than on the one that went in:
// a member marshalling under a neighbour's discriminator would otherwise round-trip undetected as
// long as their fields happened to overlap.
func TestActionRoundTripPreservesEveryMember(t *testing.T) {
	tests := map[string]struct {
		action model.Action
		kind   model.ActionKind
	}{
		"close":         {model.CloseAction{}, model.ActionClose},
		"open_link":     {model.OpenLinkAction{URL: "https://example.com/routes"}, model.ActionOpenLink},
		"navigate":      {model.NavigateAction{Destination: "routes/kamakura"}, model.ActionNavigate},
		"emit_event":    {model.EmitEventAction{EventName: "cta_pressed", Properties: map[string]string{"placement": "banner"}}, model.ActionEmitEvent},
		"set_attribute": {model.SetAttributeAction{Key: "opted_in", Value: "true"}, model.ActionSetAttribute},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.action.ActionKind(); got != tc.kind {
				t.Fatalf("ActionKind() = %q, want %q", got, tc.kind)
			}

			data, err := model.MarshalAction(tc.action)
			if err != nil {
				t.Fatalf("MarshalAction: %v", err)
			}
			decoded, err := model.UnmarshalAction(data)
			if err != nil {
				t.Fatalf("UnmarshalAction(%s): %v", data, err)
			}
			if decoded.ActionKind() != tc.kind {
				t.Errorf("decoded ActionKind() = %q, want %q", decoded.ActionKind(), tc.kind)
			}
			// reflect.DeepEqual rather than ==: EmitEventAction carries a map, which makes it an
			// uncomparable type that would panic under ==.
			if !reflect.DeepEqual(decoded, tc.action) {
				t.Errorf("decoded = %#v, want %#v", decoded, tc.action)
			}
		})
	}
}

// TestEmitEventActionRoundTripPreservesProperties is split out because EmitEventAction is the one
// member carrying a map, which the equality check above cannot cover.
func TestEmitEventActionRoundTripPreservesProperties(t *testing.T) {
	want := model.EmitEventAction{EventName: "cta_pressed", Properties: map[string]string{"placement": "banner", "step": "2"}}

	data, err := model.MarshalAction(want)
	if err != nil {
		t.Fatalf("MarshalAction: %v", err)
	}
	decoded, err := model.UnmarshalAction(data)
	if err != nil {
		t.Fatalf("UnmarshalAction: %v", err)
	}
	got, ok := decoded.(model.EmitEventAction)
	if !ok {
		t.Fatalf("UnmarshalAction returned %T, want model.EmitEventAction", decoded)
	}
	if got.EventName != want.EventName {
		t.Errorf("EventName = %q, want %q", got.EventName, want.EventName)
	}
	if len(got.Properties) != len(want.Properties) {
		t.Fatalf("Properties = %v, want %v", got.Properties, want.Properties)
	}
	for k, v := range want.Properties {
		if got.Properties[k] != v {
			t.Errorf("Properties[%q] = %q, want %q", k, got.Properties[k], v)
		}
	}
}

// TestMarshalActionRejectsAnUnknownImplementation is the encoding-side counterpart to
// UnmarshalAction's unknown-kind rejection: a type outside the union has no discriminator to write,
// so it must fail loudly rather than marshal to an envelope no decoder can read back.
func TestMarshalActionRejectsAnUnknownImplementation(t *testing.T) {
	if _, err := model.MarshalAction(unknownAction{}); err == nil {
		t.Fatal("MarshalAction(unknownAction{}): got nil error, want a rejection")
	}
}

type unknownAction struct{}

func (unknownAction) ActionKind() model.ActionKind { return "teleport" }
