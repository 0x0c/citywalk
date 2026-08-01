package model

import (
	"encoding/json"
	"fmt"
)

// Button is a label plus an ordered list of Actions (CW-0003 Unit 2). The list, rather than a single
// Action, is what lets one press both record a custom event and navigate.
type Button struct {
	Label   string
	Actions []Action
}

type buttonWire struct {
	Label   string            `json:"label"`
	Actions []json.RawMessage `json:"actions"`
}

func (b Button) MarshalJSON() ([]byte, error) {
	wire := buttonWire{Label: b.Label, Actions: make([]json.RawMessage, len(b.Actions))}
	for i, a := range b.Actions {
		raw, err := MarshalAction(a)
		if err != nil {
			return nil, fmt.Errorf("model: marshal button action %d: %w", i, err)
		}
		wire.Actions[i] = raw
	}
	return json.Marshal(wire)
}

func (b *Button) UnmarshalJSON(data []byte) error {
	var wire buttonWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("model: decode button: %w", err)
	}
	actions := make([]Action, len(wire.Actions))
	for i, raw := range wire.Actions {
		action, err := UnmarshalAction(raw)
		if err != nil {
			return fmt.Errorf("model: decode button action %d: %w", i, err)
		}
		actions[i] = action
	}
	b.Label = wire.Label
	b.Actions = actions
	return nil
}
