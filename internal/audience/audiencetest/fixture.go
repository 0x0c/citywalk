// Package audiencetest provides the one registry and environment CW-0004's own test suites — model,
// predicate, eval, sqlcompile, and the conformance suite — compile and evaluate fixture predicates
// against. Sharing a single fixture is what makes the conformance suite meaningful: row-wise and
// set-wise evaluation are compared against predicates checked against the same declarations.
package audiencetest

import (
	"github.com/google/cel-go/cel"

	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
)

// Registry returns a fixture registry covering all six attribute types and all four sources.
func Registry() *registry.Registry {
	reg, err := registry.New(
		registry.Definition{Name: "app_version", Type: registry.TypeSemVer, Source: registry.SourceChannelField},
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
		registry.Definition{Name: "total_distance_km", Type: registry.TypeNumber, Source: registry.SourceUserAttribute},
		registry.Definition{Name: "is_premium", Type: registry.TypeBool, Source: registry.SourceUserAttribute, Confidential: true},
		registry.Definition{Name: "registered_at", Type: registry.TypeTimestamp, Source: registry.SourceUserAttribute},
		registry.Definition{Name: "favorite_routes", Type: registry.TypeStringSet, Source: registry.SourceTag},
		registry.Definition{
			Name: "route_screen_views_7d", Type: registry.TypeNumber, Source: registry.SourceEventAggregate,
			AggregateGranularity: registry.AggregateGranularityDay,
		},
	)
	if err != nil {
		// The fixture registry is fixed at compile time; a construction error here is a bug in the
		// fixture itself, not a runtime condition any caller can act on.
		panic(err)
	}
	return reg
}

// Env returns the CEL environment predicates are compiled and evaluated against for Registry().
func Env() (*cel.Env, error) {
	return predicate.BuildEnv(Registry())
}
