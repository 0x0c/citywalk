package ingest

import "testing"

// TestNullIfEmptyDistinguishesAbsentFromEmpty is what keeps events_log's optional columns honest: an
// impression carries a message and variant, a custom event carries neither, and writing "" instead of
// NULL would make the second look like an event attributed to a message whose identifier is the empty
// string — which every rollup and report would then group on.
func TestNullIfEmptyDistinguishesAbsentFromEmpty(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("nullIfEmpty(\"\") = %q, want nil so the column is written as NULL", *got)
	}

	got := nullIfEmpty("message-1")
	if got == nil {
		t.Fatal(`nullIfEmpty("message-1") = nil, want a pointer to the value`)
	}
	if *got != "message-1" {
		t.Errorf(`nullIfEmpty("message-1") = %q, want "message-1"`, *got)
	}
}
