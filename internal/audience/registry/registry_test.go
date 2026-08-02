package registry_test

import (
	"testing"

	"github.com/google/cel-go/cel"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

func validEventAggregateDef() registry.Definition {
	return registry.Definition{
		Name: "route_screen_views_7d", Type: registry.TypeNumber, Source: registry.SourceEventAggregate,
		AggregateGranularity: registry.AggregateGranularityDay,
		AggregateEventName:   "route_screen_view", AggregateWindowDays: 7,
	}
}

func TestNewAcceptsAValidEventAggregateDefinition(t *testing.T) {
	if _, err := registry.New(validEventAggregateDef()); err != nil {
		t.Errorf("New() = %v, want nil", err)
	}
}

func TestNewRejectsAnEventAggregateWithNoGranularity(t *testing.T) {
	def := validEventAggregateDef()
	def.AggregateGranularity = ""
	if _, err := registry.New(def); err == nil {
		t.Error("New() = nil for an event-aggregate definition with no granularity, want an error")
	}
}

func TestNewRejectsAnEventAggregateWithNoEventName(t *testing.T) {
	def := validEventAggregateDef()
	def.AggregateEventName = ""
	if _, err := registry.New(def); err == nil {
		t.Error("New() = nil for an event-aggregate definition with no event name, want an error")
	}
}

func TestNewRejectsAnEventAggregateWithANonPositiveWindow(t *testing.T) {
	def := validEventAggregateDef()
	def.AggregateWindowDays = 0
	if _, err := registry.New(def); err == nil {
		t.Error("New() = nil for an event-aggregate definition with a zero window, want an error")
	}
}

func TestNewRejectsDuplicateNames(t *testing.T) {
	_, err := registry.New(
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
	)
	if err == nil {
		t.Error("New() = nil for duplicate attribute names, want an error")
	}
}

func TestNewRejectsAnEmptyName(t *testing.T) {
	if _, err := registry.New(registry.Definition{Name: "", Type: registry.TypeString, Source: registry.SourceChannelField}); err == nil {
		t.Error("New() = nil for an empty attribute name, want an error")
	}
}

// TestLookupFindsARegisteredNameAndMissesAnUnregisteredOne is the check CW-0004 Unit 1 leans on to
// bound what a predicate can reach: a name the registry does not describe has to come back as absent,
// not as a zero-valued definition a caller could mistake for a real string attribute.
func TestLookupFindsARegisteredNameAndMissesAnUnregisteredOne(t *testing.T) {
	reg, err := registry.New(
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	def, ok := reg.Lookup("country")
	if !ok {
		t.Fatal(`Lookup("country"): got ok=false, want the registered definition`)
	}
	if def.Type != registry.TypeString || def.Source != registry.SourceChannelField {
		t.Errorf("Lookup(\"country\") = %+v, want a channel_field-sourced string", def)
	}

	if _, ok := reg.Lookup("not_registered"); ok {
		t.Error(`Lookup("not_registered"): got ok=true, want false`)
	}
}

func TestDefinitionsReturnsEveryRegisteredDefinition(t *testing.T) {
	reg, err := registry.New(
		registry.Definition{Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField},
		registry.Definition{Name: "is_premium", Type: registry.TypeBool, Source: registry.SourceUserAttribute},
		registry.Definition{Name: "favorite_routes", Type: registry.TypeStringSet, Source: registry.SourceTag},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Definitions promises no order, so compare as a set — asserting a slice order here would pin
	// down map iteration order, which is exactly what the doc comment declines to promise.
	got := make(map[string]registry.AttributeType)
	for _, def := range reg.Definitions() {
		got[def.Name] = def.Type
	}
	want := map[string]registry.AttributeType{
		"country":         registry.TypeString,
		"is_premium":      registry.TypeBool,
		"favorite_routes": registry.TypeStringSet,
	}
	if len(got) != len(want) {
		t.Fatalf("Definitions() = %v, want %d definitions", got, len(want))
	}
	for name, wantType := range want {
		if got[name] != wantType {
			t.Errorf("Definitions()[%q].Type = %q, want %q", name, got[name], wantType)
		}
	}
}

func TestDefinitionsOnAnEmptyRegistryIsEmpty(t *testing.T) {
	reg, err := registry.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if defs := reg.Definitions(); len(defs) != 0 {
		t.Errorf("Definitions() = %v, want none", defs)
	}
}

// TestCELTypeMapsEveryAttributeType pins the two mappings CW-0004 Unit 1 calls out specifically:
// a semver attribute is declared to CEL as a plain string (ordering comes from the semver() wrapper,
// not from the type), and a string_set as a list of strings so 'in' type-checks against it.
func TestCELTypeMapsEveryAttributeType(t *testing.T) {
	tests := map[registry.AttributeType]*cel.Type{
		registry.TypeString:    cel.StringType,
		registry.TypeSemVer:    cel.StringType,
		registry.TypeNumber:    cel.DoubleType,
		registry.TypeBool:      cel.BoolType,
		registry.TypeTimestamp: cel.TimestampType,
		registry.TypeStringSet: cel.ListType(cel.StringType),
	}

	for attrType, want := range tests {
		t.Run(string(attrType), func(t *testing.T) {
			got, err := registry.CELType(attrType)
			if err != nil {
				t.Fatalf("CELType(%q): %v", attrType, err)
			}
			if !got.IsExactType(want) {
				t.Errorf("CELType(%q) = %v, want %v", attrType, got, want)
			}
		})
	}
}

// TestCELTypeRejectsAnUnknownAttributeType keeps an unrecognized type from silently becoming some
// default CEL type: the environment would then type-check predicates against a declaration nobody
// wrote.
func TestCELTypeRejectsAnUnknownAttributeType(t *testing.T) {
	if _, err := registry.CELType(registry.AttributeType("geopoint")); err == nil {
		t.Fatal(`CELType("geopoint"): got nil error, want a rejection`)
	}
}
