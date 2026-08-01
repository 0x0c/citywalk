package model

import "time"

// Trigger is the condition on an occurrence that makes the platform attempt to display a message
// (CW-0003 Unit 1). The predicate, when present, is evaluated by the device against event
// properties — CW-0002's split keeps trigger evaluation off the server entirely.
type Trigger struct {
	ID             string
	Kind           string
	OccurrenceGoal int
	EventPredicate string // CEL, per CW-0010 Unit 2; empty means the trigger has no property condition.
}

// ScreenFilterMode says whether DisplayCondition.Screens is an allowlist or a denylist.
type ScreenFilterMode string

const (
	ScreenFilterAllow ScreenFilterMode = "allow"
	ScreenFilterDeny  ScreenFilterMode = "deny"
)

// ConnectivityRequirement is the network condition a DisplayCondition can demand before a message
// that already fired its Trigger is allowed to actually appear.
type ConnectivityRequirement string

const (
	ConnectivityAny    ConnectivityRequirement = "any"
	ConnectivityOnline ConnectivityRequirement = "online"
)

// DisplayCondition is evaluated after a Trigger fires and decides whether the message may actually
// appear (CW-0003 Unit 1). Like Trigger, this runs entirely on the device.
type DisplayCondition struct {
	Delay                time.Duration
	ScreenFilterMode     ScreenFilterMode
	Screens              []string
	ConnectivityRequired ConnectivityRequirement
}

// ControlPolicy is the per-message governance CW-0003 Unit 1 names: caps, the minimum interval
// between impressions, and whether the message opts out of the project-wide cap (CW-0007).
type ControlPolicy struct {
	PerMessageCap        int
	MinIntervalBetween   time.Duration
	ExemptFromProjectCap bool
	// RequiresServerConfirmation is CW-0002 Unit 4's escape hatch: the device calls one endpoint
	// immediately before displaying and shows the message only on an explicit yes. Restraint is the
	// point — this defaults false, and the administrative interface (not built yet) is what would
	// make setting it a visible, per-campaign exception rather than a default.
	RequiresServerConfirmation bool
}
