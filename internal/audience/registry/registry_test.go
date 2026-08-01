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
