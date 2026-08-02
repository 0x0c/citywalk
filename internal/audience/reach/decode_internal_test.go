package reach

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

func decodeFixtureRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.New(
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
		registry.Definition{Name: "total_distance_km", Type: registry.TypeNumber, Source: registry.SourceUserAttribute},
		registry.Definition{Name: "is_premium", Type: registry.TypeBool, Source: registry.SourceUserAttribute},
		registry.Definition{Name: "registered_at", Type: registry.TypeTimestamp, Source: registry.SourceUserAttribute},
		registry.Definition{Name: "favorite_routes", Type: registry.TypeStringSet, Source: registry.SourceTag},
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return reg
}

// TestDecodeAttributesConvertsTheTwoTypesJSONCannotCarry is the gap this function exists to close:
// jsonb round-trips a timestamp as an RFC 3339 string and a string_set as []any, and CEL's evaluator
// accepts neither — a predicate comparing registered_at would fail at evaluation, per channel, with
// nothing in the predicate itself to blame.
func TestDecodeAttributesConvertsTheTwoTypesJSONCannotCarry(t *testing.T) {
	raw := map[string]any{
		"registered_at":   "2026-01-05T12:00:00Z",
		"favorite_routes": []any{"kamakura", "hakone"},
	}

	got, err := decodeAttributes(decodeFixtureRegistry(t), raw)
	if err != nil {
		t.Fatalf("decodeAttributes: %v", err)
	}

	registered, ok := got["registered_at"].(time.Time)
	if !ok {
		t.Fatalf("registered_at decoded as %T, want time.Time", got["registered_at"])
	}
	if want := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC); !registered.Equal(want) {
		t.Errorf("registered_at = %v, want %v", registered, want)
	}

	routes, ok := got["favorite_routes"].([]string)
	if !ok {
		t.Fatalf("favorite_routes decoded as %T, want []string", got["favorite_routes"])
	}
	if len(routes) != 2 || routes[0] != "kamakura" || routes[1] != "hakone" {
		t.Errorf("favorite_routes = %v, want [kamakura hakone]", routes)
	}
}

// TestDecodeAttributesPassesThroughTypesJSONAlreadyMatches keeps the conversion narrow: a JSON string,
// number, or boolean already decodes to the Go type CEL wants, so touching them would only introduce
// a way to get them wrong.
func TestDecodeAttributesPassesThroughTypesJSONAlreadyMatches(t *testing.T) {
	raw := map[string]any{"country": "JP", "total_distance_km": 42.5, "is_premium": true}

	got, err := decodeAttributes(decodeFixtureRegistry(t), raw)
	if err != nil {
		t.Fatalf("decodeAttributes: %v", err)
	}
	if got["country"] != "JP" || got["total_distance_km"] != 42.5 || got["is_premium"] != true {
		t.Errorf("decodeAttributes = %v, want the three values unchanged", got)
	}
}

// TestDecodeAttributesDropsNamesTheRegistryNoLongerDescribes is what lets a registry retire an
// attribute without a migration over every channel row: leftover jsonb keys are skipped, not
// rejected, since no predicate compiled against the current registry can reference them anyway.
func TestDecodeAttributesDropsNamesTheRegistryNoLongerDescribes(t *testing.T) {
	raw := map[string]any{"country": "JP", "retired_attribute": "whatever shape"}

	got, err := decodeAttributes(decodeFixtureRegistry(t), raw)
	if err != nil {
		t.Fatalf("decodeAttributes: %v", err)
	}
	if _, ok := got["retired_attribute"]; ok {
		t.Errorf("decodeAttributes = %v, want the unregistered key dropped", got)
	}
	if got["country"] != "JP" {
		t.Errorf("decodeAttributes = %v, want the registered key kept", got)
	}
}

// TestDecodeAttributesRejectsAValueThatContradictsItsRegisteredType surfaces a genuine data problem
// rather than evaluating the predicate against a value of the wrong Go type: a silent skip here would
// turn a corrupt row into a channel that merely fails the predicate.
func TestDecodeAttributesRejectsAValueThatContradictsItsRegisteredType(t *testing.T) {
	tests := map[string]map[string]any{
		"timestamp that is not a string":    {"registered_at": 1767614400},
		"timestamp that is not RFC 3339":    {"registered_at": "5 January 2026"},
		"string_set that is not a list":     {"favorite_routes": "kamakura"},
		"string_set with a non-string item": {"favorite_routes": []any{"kamakura", 7}},
	}

	reg := decodeFixtureRegistry(t)
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := decodeAttributes(reg, raw); err == nil {
				t.Fatalf("decodeAttributes(%v) = %v, want an error", raw, got)
			}
		})
	}
}
