package model

import (
	"encoding/json"
	"fmt"
)

// ActionKind discriminates the Action tagged union (CW-0003 Unit 2).
type ActionKind string

const (
	ActionClose        ActionKind = "close"
	ActionOpenLink     ActionKind = "open_link"
	ActionNavigate     ActionKind = "navigate"
	ActionEmitEvent    ActionKind = "emit_event"
	ActionSetAttribute ActionKind = "set_attribute"
)

// Action is one member of the tagged union a button press runs. A Button carries an ordered list of
// these, so one press can both emit a custom event and navigate (CW-0003 Unit 2).
type Action interface {
	ActionKind() ActionKind
}

type CloseAction struct{}

func (CloseAction) ActionKind() ActionKind { return ActionClose }

type OpenLinkAction struct {
	URL string `json:"url"`
}

func (OpenLinkAction) ActionKind() ActionKind { return ActionOpenLink }

type NavigateAction struct {
	Destination string `json:"destination"`
}

func (NavigateAction) ActionKind() ActionKind { return ActionNavigate }

type EmitEventAction struct {
	EventName  string            `json:"event_name"`
	Properties map[string]string `json:"properties,omitempty"`
}

func (EmitEventAction) ActionKind() ActionKind { return ActionEmitEvent }

type SetAttributeAction struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (SetAttributeAction) ActionKind() ActionKind { return ActionSetAttribute }

// actionEnvelope is the wire shape every Action marshals to and unmarshals from: the discriminator
// plus that member's own fields, flattened via the anonymous embeds below.
type actionEnvelope struct {
	Kind ActionKind `json:"kind"`
	OpenLinkAction
	NavigateAction
	EmitEventAction
	SetAttributeAction
}

// MarshalAction encodes a into the tagged-union wire shape, injecting its discriminator.
func MarshalAction(a Action) ([]byte, error) {
	env := actionEnvelope{Kind: a.ActionKind()}
	switch v := a.(type) {
	case CloseAction:
	case OpenLinkAction:
		env.OpenLinkAction = v
	case NavigateAction:
		env.NavigateAction = v
	case EmitEventAction:
		env.EmitEventAction = v
	case SetAttributeAction:
		env.SetAttributeAction = v
	default:
		return nil, fmt.Errorf("model: unknown action type %T", a)
	}
	return json.Marshal(env)
}

// UnmarshalAction decodes the tagged-union wire shape, rejecting any kind this build does not know
// rather than guessing — an unrecognized action is a save-time rejection (CW-0003 Unit 5), not a
// best-effort pass-through.
func UnmarshalAction(data []byte) (Action, error) {
	var env actionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("model: decode action: %w", err)
	}
	switch env.Kind {
	case ActionClose:
		return CloseAction{}, nil
	case ActionOpenLink:
		return env.OpenLinkAction, nil
	case ActionNavigate:
		return env.NavigateAction, nil
	case ActionEmitEvent:
		return env.EmitEventAction, nil
	case ActionSetAttribute:
		return env.SetAttributeAction, nil
	default:
		return nil, fmt.Errorf("model: unknown action kind %q", env.Kind)
	}
}
