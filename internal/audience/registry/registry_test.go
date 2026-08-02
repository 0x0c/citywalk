package registry_test

import (
	"testing"

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

// TestGranularityCarriesAcceptsTodaysOnlyGranularity proves CW-0004 Unit 5's type-checker rule is a
// no-op against the registry as it exists today, where "day" is the only granularity a Definition can
// carry and every window is a whole number of days: a day-level request against a day-level rollup
// always carries.
func TestGranularityCarriesAcceptsTodaysOnlyGranularity(t *testing.T) {
	carries, err := registry.GranularityCarries(registry.AggregateGranularityDay, registry.AggregateGranularityDay)
	if err != nil {
		t.Fatalf("GranularityCarries: %v, want nil", err)
	}
	if !carries {
		t.Error("GranularityCarries(day, day) = false, want true")
	}
}

// TestGranularityCarriesRejectsAFinerRequestThanADayLevelRollup exercises the general comparison
// directly with the roadmap's own example: an hour-level request against a rollup that only carries
// day-level buckets cannot be answered, even though no Definition registered today can produce this
// request — this is what makes the rule already correct once a second, finer granularity exists for a
// predicate to ask for by mistake.
func TestGranularityCarriesRejectsAFinerRequestThanADayLevelRollup(t *testing.T) {
	carries, err := registry.GranularityCarries(registry.AggregateGranularityDay, "hour")
	if err != nil {
		t.Fatalf("GranularityCarries: %v, want nil", err)
	}
	if carries {
		t.Error("GranularityCarries(day, hour) = true, want false: a day-level rollup cannot answer an hour-level request")
	}
}

// TestGranularityCarriesAcceptsACoarserRequestThanAFinerRollup is the mirror case: an hour-level
// rollup carries more than enough precision to answer a day-level request.
func TestGranularityCarriesAcceptsACoarserRequestThanAFinerRollup(t *testing.T) {
	carries, err := registry.GranularityCarries("hour", registry.AggregateGranularityDay)
	if err != nil {
		t.Fatalf("GranularityCarries: %v, want nil", err)
	}
	if !carries {
		t.Error("GranularityCarries(hour, day) = false, want true: an hour-level rollup answers a day-level request")
	}
}

func TestGranularityCarriesRejectsAnUnknownGranularity(t *testing.T) {
	if _, err := registry.GranularityCarries("fortnight", registry.AggregateGranularityDay); err == nil {
		t.Error("GranularityCarries(\"fortnight\", day) = nil error, want an error for an unrecognized granularity")
	}
}
