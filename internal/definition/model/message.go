package model

import "time"

// MessageState is the campaign lifecycle CW-0003 Unit 1 and requirements.md FR-MSG-01 define. A
// transition only ever moves forward through this list.
type MessageState string

const (
	MessageStateDraft     MessageState = "draft"
	MessageStateScheduled MessageState = "scheduled"
	MessageStateActive    MessageState = "active"
	MessageStatePaused    MessageState = "paused"
	MessageStateCompleted MessageState = "completed"
	MessageStateArchived  MessageState = "archived"
)

// forwardMessageStates lists every state a Message may still transition into from the key state,
// including itself (a no-op save). Validate rejects any transition not listed here.
var forwardMessageStates = map[MessageState][]MessageState{
	MessageStateDraft:     {MessageStateDraft, MessageStateScheduled, MessageStateActive, MessageStateArchived},
	MessageStateScheduled: {MessageStateScheduled, MessageStateActive, MessageStatePaused, MessageStateArchived},
	MessageStateActive:    {MessageStateActive, MessageStatePaused, MessageStateCompleted, MessageStateArchived},
	MessageStatePaused:    {MessageStatePaused, MessageStateActive, MessageStateCompleted, MessageStateArchived},
	MessageStateCompleted: {MessageStateCompleted, MessageStateArchived},
	MessageStateArchived:  {MessageStateArchived},
}

// CanTransition reports whether a Message in state from is allowed to move to state to, including
// the no-op case from == to (FR-MSG-01: "a transition only moves forward").
func (from MessageState) CanTransition(to MessageState) bool {
	for _, allowed := range forwardMessageStates[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Window is the delivery window a Message is eligible to display within.
type Window struct {
	Start time.Time
	End   time.Time
}

// Message is the campaign entity (CW-0003 Unit 1): everything a channel cannot see. AudienceRef
// never leaves the server — CW-0002's delivery boundary depends on that holding, so it carries no
// json tag and is never included in any payload encoding.
type Message struct {
	ID       string
	Name     string
	State    MessageState
	Priority int
	Window   Window
	// Version increments on every save. CW-0001 Unit 1's full publish-a-new-version-rather-than-
	// mutate workflow isn't built yet — this is the minimal counter CW-0002 Unit 2's payload
	// contract needs a device to detect a change by, ahead of that fuller mechanism.
	Version         int
	AudienceRef     string
	ControlPolicy   ControlPolicy
	HoldoutFraction float64
	// ExperimentSalt is CW-0008 Unit 2's per-experiment salt: generated once, server-side, when the
	// message is first inserted (a column default, not application code — see
	// migrations/0007_experiment_salt.sql), and never reused or recomputed thereafter.
	ExperimentSalt    string
	ConversionEvent   string
	Triggers          []Trigger
	DisplayConditions []DisplayCondition
	Variants          []Variant
}
