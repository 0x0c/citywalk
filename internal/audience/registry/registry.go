// Package registry implements CW-0004 Unit 1: the typed attribute registry every audience predicate
// is checked and evaluated against. A name absent from the registry cannot appear in a predicate, so
// the registry both types the predicate and bounds the data it can reach.
package registry

import (
	"fmt"

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
		if def.Source == SourceEventAggregate && def.AggregateGranularity == "" {
			return nil, fmt.Errorf("registry: attribute %q sources an event aggregate but names no granularity", def.Name)
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
