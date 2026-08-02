// Package registry implements CW-0004 Unit 1: the typed attribute registry every audience predicate
// is checked and evaluated against. A name absent from the registry cannot appear in a predicate, so
// the registry both types the predicate and bounds the data it can reach.
package registry

import (
	"fmt"
	"time"

	"github.com/google/cel-go/cel"
)

// AttributeType is one of the six value types CW-0004 Unit 1 names.
type AttributeType string

const (
	TypeString    AttributeType = "string"
	TypeNumber    AttributeType = "number"
	TypeBool      AttributeType = "bool"
	TypeTimestamp AttributeType = "timestamp"
	TypeSemVer    AttributeType = "semver"
	TypeStringSet AttributeType = "string_set"
)

// Source names where an attribute's value comes from (CW-0004 Unit 1).
type Source string

const (
	SourceChannelField   Source = "channel_field"
	SourceUserAttribute  Source = "user_attribute"
	SourceTag            Source = "tag"
	SourceEventAggregate Source = "event_aggregate"
)

// AggregateGranularity is the bucket width an event-aggregate-sourced attribute's rollup carries.
// CW-0004 Unit 5: the registry records this so the type checker can reject a windowed condition
// asking for finer precision than the rollup stores.
type AggregateGranularity string

const AggregateGranularityDay AggregateGranularity = "day"

// aggregateGranularityBucket is the wall-clock span one rollup bucket covers, for every granularity
// this package knows how to order. AggregateGranularityDay is the only value a real Definition can
// use today; "hour" and "week" are recognized here, unexported and without constants of their own,
// purely so GranularityCarries is a genuine ordering rather than a comparison special-cased to the
// single value currently in production use — CW-0004 Unit 5's type-checker rejection has nothing to
// compare against until a second granularity is registered, and this is what keeps the comparison
// already correct on the day that happens, instead of needing to be rewritten.
var aggregateGranularityBucket = map[AggregateGranularity]time.Duration{
	"hour":                  time.Hour,
	AggregateGranularityDay: 24 * time.Hour,
	"week":                  7 * 24 * time.Hour,
}

// GranularityCarries reports whether an event-aggregate attribute registered at have's granularity
// carries enough precision to answer a condition that requests want-level precision — true when
// have's rollup bucket is no larger (no coarser) than want's. CW-0004 Unit 5: predicate.Compile calls
// this to reject a condition asking for finer precision than an event-aggregate attribute's registered
// granularity carries (e.g. an hour-level request against a day-level rollup). An unknown granularity
// on either side is an error rather than a silent pass, matching this package's other validation: a
// value this code cannot reason about is rejected, not accepted by default.
func GranularityCarries(have, want AggregateGranularity) (bool, error) {
	haveBucket, ok := aggregateGranularityBucket[have]
	if !ok {
		return false, fmt.Errorf("registry: unknown aggregate granularity %q", have)
	}
	wantBucket, ok := aggregateGranularityBucket[want]
	if !ok {
		return false, fmt.Errorf("registry: unknown aggregate granularity %q", want)
	}
	return haveBucket <= wantBucket, nil
}

// Definition describes one name a predicate may reference.
type Definition struct {
	// Name is the identifier a predicate uses, and the jsonb key under channels.attributes that
	// holds a channel_field/user_attribute/tag-sourced value (sqlcompile reads this convention).
	Name   string
	Type   AttributeType
	Source Source
	// Confidential marks an attribute that may appear in an audience predicate (server-evaluated)
	// and must never appear in a trigger predicate (device-evaluated) — the flag CW-0002's delivery
	// boundary depends on. This registry does not itself enforce the trigger-side restriction; it
	// exists so the code that does (the delivery service assembling a device payload) has something
	// to check.
	Confidential bool
	// AggregateGranularity is set only for Source == SourceEventAggregate, and names the rollup
	// bucket width (CW-0004 Unit 5).
	AggregateGranularity AggregateGranularity
	// AggregateEventName and AggregateWindowDays apply only when Source == SourceEventAggregate: the
	// event name CW-0009's targeting_rollup groups on, and how many trailing days the sum covers.
	// Both are baked into the definition rather than left for a predicate to parameterize — a
	// definition named "route_screen_views_7d" fixes the event and the window at registration time,
	// so the predicate itself, `route_screen_views_7d >= 3`, needs no runtime window argument.
	AggregateEventName  string
	AggregateWindowDays int
}

// Registry is the immutable set of attributes a predicate environment is built against.
type Registry struct {
	defs map[string]Definition
}

// New validates defs — no duplicate names, and every SourceEventAggregate definition names its
// granularity — and returns the Registry built from them.
func New(defs ...Definition) (*Registry, error) {
	byName := make(map[string]Definition, len(defs))
	for _, def := range defs {
		if def.Name == "" {
			return nil, fmt.Errorf("registry: attribute definition has an empty name")
		}
		if _, exists := byName[def.Name]; exists {
			return nil, fmt.Errorf("registry: duplicate attribute name %q", def.Name)
		}
		if def.Source == SourceEventAggregate {
			if def.AggregateGranularity == "" {
				return nil, fmt.Errorf("registry: attribute %q sources an event aggregate but names no granularity", def.Name)
			}
			if def.AggregateEventName == "" {
				return nil, fmt.Errorf("registry: attribute %q sources an event aggregate but names no event", def.Name)
			}
			if def.AggregateWindowDays <= 0 {
				return nil, fmt.Errorf("registry: attribute %q sources an event aggregate but has a non-positive window (%d days)", def.Name, def.AggregateWindowDays)
			}
		}
		byName[def.Name] = def
	}
	return &Registry{defs: byName}, nil
}

// Lookup returns the definition for name, or false if name is not registered — the check that keeps
// a predicate from referencing anything the registry does not describe.
func (r *Registry) Lookup(name string) (Definition, bool) {
	def, ok := r.defs[name]
	return def, ok
}

// Definitions returns every registered definition, in no particular order.
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, def)
	}
	return out
}

// CELType returns the CEL type a predicate environment declares for t. SemVer attributes are typed
// as CEL strings — see the semver() function predicate authors must wrap a SemVer identifier in for
// a correctly-ordered comparison — and StringSet as a CEL list of strings.
func CELType(t AttributeType) (*cel.Type, error) {
	switch t {
	case TypeString, TypeSemVer:
		return cel.StringType, nil
	case TypeNumber:
		return cel.DoubleType, nil
	case TypeBool:
		return cel.BoolType, nil
	case TypeTimestamp:
		return cel.TimestampType, nil
	case TypeStringSet:
		return cel.ListType(cel.StringType), nil
	default:
		return nil, fmt.Errorf("registry: unknown attribute type %q", t)
	}
}
