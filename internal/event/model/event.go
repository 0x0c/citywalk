// Package model implements CW-0009 Unit 1: the event envelope every measurement and targeting event
// shares, the closed sets (event kind, suppression reason) Unit 2's acceptance path validates
// against, and the clock-offset tolerance the two-timestamp rule uses to decide when a device's
// self-reported time can be trusted for anything ordering-sensitive.
package model

import (
	"fmt"
	"time"
)

// Kind is the closed set of event categories the platform recognizes. Acceptance (Unit 2) rejects any
// event naming a Kind outside this set — "no unknown event type" is enforced here, not left to each
// consumer to discover on its own.
type Kind string

const (
	// KindImpression is a message actually drawn on screen.
	KindImpression Kind = "impression"
	// KindButtonPress is a button press resolution.
	KindButtonPress Kind = "button_press"
	// KindDismissal is a user-initiated close.
	KindDismissal Kind = "dismissal"
	// KindAutoClose is an automatic close after the configured delay.
	KindAutoClose Kind = "auto_close"
	// KindSuppression is a message that qualified but was withheld by governance (CW-0007).
	KindSuppression Kind = "suppression"
	// KindHoldoutQualified is a channel that qualified for a campaign but landed in a holdout
	// (CW-0008 Unit 4) and so received no content.
	KindHoldoutQualified Kind = "holdout_qualified"
	// KindCustom is an application-defined event: a screen view, a launch, or a campaign-designated
	// conversion event. Name carries the application's own event name for this kind.
	KindCustom Kind = "custom"
)

// impressionFamily is the set of kinds Unit 1 requires a MessageID for: every kind that names a
// specific campaign outcome, whether or not a variant was ever selected.
var impressionFamily = map[Kind]bool{
	KindImpression:       true,
	KindButtonPress:      true,
	KindDismissal:        true,
	KindAutoClose:        true,
	KindSuppression:      true,
	KindHoldoutQualified: true,
}

// variantRequired is the subset of impressionFamily that additionally requires a VariantID: a
// suppression or a holdout qualification names the campaign a channel was withheld from, but no
// variant was ever selected for either, so requiring one would reject the very events these two kinds
// exist to record.
var variantRequired = map[Kind]bool{
	KindImpression:  true,
	KindButtonPress: true,
	KindDismissal:   true,
	KindAutoClose:   true,
}

// knownKinds is the closed set Validate checks Kind against.
var knownKinds = map[Kind]bool{
	KindImpression:       true,
	KindButtonPress:      true,
	KindDismissal:        true,
	KindAutoClose:        true,
	KindSuppression:      true,
	KindHoldoutQualified: true,
	KindCustom:           true,
}

// SuppressionReason is CW-0007 Unit 6's closed set, anticipated here because CW-0009 Unit 1 requires
// it to validate a suppression event's reason before CW-0007 exists to emit one. CW-0007, when
// implemented, must emit exactly these values rather than defining its own set.
type SuppressionReason string

const (
	ReasonPerMessageCap         SuppressionReason = "per_message_cap"
	ReasonMinimumInterval       SuppressionReason = "minimum_interval"
	ReasonProjectBudget         SuppressionReason = "project_budget"
	ReasonLostOnPriority        SuppressionReason = "lost_on_priority"
	ReasonDisplayConditionUnmet SuppressionReason = "display_condition_unmet"
	ReasonCooldown              SuppressionReason = "cooldown"
	ReasonCancellationTrigger   SuppressionReason = "cancellation_trigger"
	ReasonExpiry                SuppressionReason = "expiry"
)

var knownSuppressionReasons = map[SuppressionReason]bool{
	ReasonPerMessageCap:         true,
	ReasonMinimumInterval:       true,
	ReasonProjectBudget:         true,
	ReasonLostOnPriority:        true,
	ReasonDisplayConditionUnmet: true,
	ReasonCooldown:              true,
	ReasonCancellationTrigger:   true,
	ReasonExpiry:                true,
}

// Event is CW-0009 Unit 1's envelope: the fields every event carries, plus the impression-family
// fields that apply only to Kind values in impressionFamily.
type Event struct {
	// ID is a client-generated, time-ordered (UUIDv7) identifier. Unit 4's deduplication collapses
	// rows sharing this value, so a device may resend a batch after a timeout without double-counting.
	ID string
	// ChannelID identifies the device (CW-0004's channel entity).
	ChannelID string
	Kind      Kind
	// Name is the application-defined event name for KindCustom. Non-custom kinds ignore Name; the
	// Kind itself is the name.
	Name string
	// DeviceTime is when the event occurred on the device, per the device's own clock. Manipulable
	// and sometimes wrong, but it is what analysis means (Unit 1).
	DeviceTime time.Time
	// ServerTime is when the event was received. Trustworthy but late for an event accumulated
	// offline. Ingestion sets this; a client-supplied value here is ignored.
	ServerTime time.Time
	Properties map[string]any

	// MessageID and VariantID apply only to impressionFamily kinds: the campaign and the variant
	// recorded at display (CW-0008's pinned assignment).
	MessageID string
	VariantID string
	// SuppressionReason applies only to KindSuppression, from the closed set above.
	SuppressionReason SuppressionReason
}

// Validate checks e's shape against the closed sets and the impression-family field requirements.
// Acceptance (Unit 2) rejects an event that fails this before it reaches the log.
func (e Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("event: id is required")
	}
	if e.ChannelID == "" {
		return fmt.Errorf("event: channel_id is required")
	}
	if !knownKinds[e.Kind] {
		return fmt.Errorf("event: unknown kind %q", e.Kind)
	}
	if e.Kind == KindCustom && e.Name == "" {
		return fmt.Errorf("event: name is required for kind %q", KindCustom)
	}
	if e.DeviceTime.IsZero() {
		return fmt.Errorf("event: device_time is required")
	}

	if impressionFamily[e.Kind] && e.MessageID == "" {
		return fmt.Errorf("event: message_id is required for kind %q", e.Kind)
	}
	if variantRequired[e.Kind] && e.VariantID == "" {
		return fmt.Errorf("event: variant_id is required for kind %q", e.Kind)
	}
	if e.Kind == KindSuppression && !knownSuppressionReasons[e.SuppressionReason] {
		return fmt.Errorf("event: unknown suppression reason %q", e.SuppressionReason)
	}

	return nil
}

// EventName returns the name the event is grouped by for the targeting rollup (Unit 5): the
// application's own name for KindCustom, and the Kind itself otherwise.
func (e Event) EventName() string {
	if e.Kind == KindCustom {
		return e.Name
	}
	return string(e.Kind)
}

// Clock-offset tolerance for Unit 1's two-timestamp rule: how far a device-reported timestamp may
// diverge from the server's receipt time before it is treated as untrustworthy rather than merely
// late. docs/requirements.md's constraint #2 and its SDK responsibility #9 require that a device
// whose offset "exceeds a threshold is detected and logged," but name no concrete threshold, so
// these two are an engineering judgement call, documented here rather than left implicit:
//
//   - MaxFutureSkew is tight. There is no legitimate reason for a device's clock to read ahead of the
//     server that just received the event by more than ordinary clock drift — a few minutes covers
//     that generously — so anything past it is either a manipulated clock or a bug, not a real event
//     from the future.
//   - MaxPastSkew is loose. A real device accumulates events offline before it gets a chance to send
//     them (Unit 1's own motivating case for keeping both timestamps), so the bound only needs to
//     catch a clock that is simply wrong — stuck at an epoch default, or years off — rather than a
//     long but genuine offline backlog.
const (
	MaxFutureSkew = 5 * time.Minute
	MaxPastSkew   = 30 * 24 * time.Hour
)

// clockOffset is serverTime minus deviceTime: positive when the device is behind (the ordinary case,
// including a genuine offline backlog), negative when the device's clock reads ahead of the server.
func clockOffset(deviceTime, serverTime time.Time) time.Duration {
	return serverTime.Sub(deviceTime)
}

// ClockSkewImplausible reports whether deviceTime diverges from serverTime by more than MaxFutureSkew
// or MaxPastSkew — a clock reading into the future, or reporting a backlog implausibly older than any
// real one. Acceptance (Unit 2) flags such an event rather than rejecting it, so the divergence is
// observable (docs/requirements.md's "detected and logged") without adding a round trip to the
// device; EffectiveTime is what keeps the event from being trusted verbatim downstream.
func ClockSkewImplausible(deviceTime, serverTime time.Time) bool {
	offset := clockOffset(deviceTime, serverTime)
	return offset < -MaxFutureSkew || offset > MaxPastSkew
}

// EffectiveTime is the time Unit 1's two-timestamp rule treats as authoritative for anything
// ordering- or bucketing-sensitive: deviceTime, unless ClockSkewImplausible says it cannot be
// trusted, in which case serverTime — received directly by the server, not self-reported — takes
// over. deviceTime itself is never altered; callers that need it for display or debugging keep
// reading it as stored.
//
// This is deliberately conservative on the past side: an event within MaxPastSkew still buckets by
// its own device time, since that is what "a late arrival lands in the day it belongs to" means. A
// scheduled recomputation of the last several days' aggregates — so a bucket already written under an
// earlier, incomplete read of the log gets corrected once the late arrival shows up — is still open
// against Unit 1, pending the job queue/scheduler CW-0010 Unit 8 does not yet provide.
func EffectiveTime(deviceTime, serverTime time.Time) time.Time {
	if ClockSkewImplausible(deviceTime, serverTime) {
		return serverTime
	}
	return deviceTime
}
