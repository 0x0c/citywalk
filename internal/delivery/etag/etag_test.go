package etag_test

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/delivery/etag"
	"github.com/0x0c/citywalk/internal/delivery/payload"
)

func baseEntries() []payload.Entry {
	return []payload.Entry{
		{
			MessageID: "msg-1", Version: 1, VariantID: "var-1",
			SchemaVersion: model.SchemaVersion{Major: 1, Minor: 0},
			ControlPolicy: model.ControlPolicy{PerMessageCap: 3, MinIntervalBetween: time.Hour},
		},
		{
			MessageID: "msg-2", Version: 1, VariantID: "var-2",
			SchemaVersion: model.SchemaVersion{Major: 1, Minor: 0},
			ControlPolicy: model.ControlPolicy{PerMessageCap: 1, MinIntervalBetween: 30 * time.Minute},
		},
	}
}

func TestComputeIsStableUnderReordering(t *testing.T) {
	a := baseEntries()
	b := []payload.Entry{a[1], a[0]} // same entries, reversed order

	if etag.Compute(a) != etag.Compute(b) {
		t.Error("Compute differs when the same entries are given in a different order")
	}
}

func TestComputeChangesWhenAMessageVersionChanges(t *testing.T) {
	a := baseEntries()
	before := etag.Compute(a)

	b := baseEntries()
	b[0].Version = 2
	after := etag.Compute(b)

	if before == after {
		t.Error("Compute did not change when a message's version changed")
	}
}

func TestComputeChangesWhenAControlPolicyValueChanges(t *testing.T) {
	a := baseEntries()
	before := etag.Compute(a)

	b := baseEntries()
	b[0].ControlPolicy.PerMessageCap = 99
	after := etag.Compute(b)

	if before == after {
		t.Error("Compute did not change when a control policy value changed")
	}
}

func TestComputeIsUnchangedWhenNothingRelevantChanges(t *testing.T) {
	a := baseEntries()
	b := baseEntries()

	if etag.Compute(a) != etag.Compute(b) {
		t.Error("Compute differs for two calls over equivalent entries")
	}
}

func TestComputeDistinguishesFieldBoundaries(t *testing.T) {
	// "msg-1" + version 12 must not hash the same as "msg-112" + version <absent>: a naive
	// delimiter-free concatenation would collide here.
	a := []payload.Entry{{MessageID: "msg-1", Version: 12}}
	b := []payload.Entry{{MessageID: "msg-112", Version: 0}}

	if etag.Compute(a) == etag.Compute(b) {
		t.Error("Compute collided across a field boundary")
	}
}
