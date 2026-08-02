package store

import "testing"

// TestNullIfEmptyDistinguishesAbsentFromEmpty keeps an unset optional column out of the "set to the
// empty string" state: the two are different facts, and only NULL round-trips back through
// stringOrEmpty as genuinely absent.
func TestNullIfEmptyDistinguishesAbsentFromEmpty(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("nullIfEmpty(\"\") = %q, want nil so the column is written as NULL", *got)
	}

	got := nullIfEmpty("segment-1")
	if got == nil || *got != "segment-1" {
		t.Errorf(`nullIfEmpty("segment-1") = %v, want a pointer to "segment-1"`, got)
	}
}

// TestStringOrEmptyReadsANullColumnBackAsTheZeroString is nullIfEmpty's inverse on the read path: a
// NULL column has to become "" rather than panic on a nil dereference, since the model types carry
// plain strings and have no separate absent state to decode into.
func TestStringOrEmptyReadsANullColumnBackAsTheZeroString(t *testing.T) {
	if got := stringOrEmpty(nil); got != "" {
		t.Errorf("stringOrEmpty(nil) = %q, want the empty string", got)
	}

	value := "segment-1"
	if got := stringOrEmpty(&value); got != value {
		t.Errorf("stringOrEmpty(&%q) = %q, want %q", value, got, value)
	}
}

// TestNullIfEmptyAndStringOrEmptyRoundTrip states the pairing the two helpers exist to guarantee:
// whatever goes into a nullable column comes back unchanged, empty string included.
func TestNullIfEmptyAndStringOrEmptyRoundTrip(t *testing.T) {
	for _, want := range []string{"", "segment-1"} {
		if got := stringOrEmpty(nullIfEmpty(want)); got != want {
			t.Errorf("stringOrEmpty(nullIfEmpty(%q)) = %q, want %q", want, got, want)
		}
	}
}
