package model_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/definition/model"
)

// TestSupportsMajorIgnoresMinor is CW-0003 Unit 4's additive-Minor rule stated as a test: within one
// Major, a device that knows a lower Minor than the content carries can still render it by ignoring
// the fields it does not recognize, so Minor must play no part in the delivery-side check.
func TestSupportsMajorIgnoresMinor(t *testing.T) {
	for _, minor := range []int{0, 1, 7, 99} {
		v := model.SchemaVersion{Major: model.CurrentMajor, Minor: minor}
		if !v.SupportsMajor(model.CurrentMajor) {
			t.Errorf("SchemaVersion{%d, %d}.SupportsMajor(%d) = false, want true", v.Major, v.Minor, model.CurrentMajor)
		}
	}
}

// TestSupportsMajorRejectsADifferentMajor is the other half of Unit 4: a Major change is breaking, so
// content written against one Major is never handed to a device that declared another. Both
// directions are checked — a device ahead of the content is as unsupported as one behind it, since
// parallel emission, not in-place migration, is how a second Major arrives.
func TestSupportsMajorRejectsADifferentMajor(t *testing.T) {
	v := model.SchemaVersion{Major: 2, Minor: 0}

	if v.SupportsMajor(1) {
		t.Error("SchemaVersion{2, 0}.SupportsMajor(1) = true, want false (the device is behind the content)")
	}
	if v.SupportsMajor(3) {
		t.Error("SchemaVersion{2, 0}.SupportsMajor(3) = true, want false (the device is ahead of the content)")
	}
}

func TestSchemaVersionString(t *testing.T) {
	tests := map[string]model.SchemaVersion{
		"1.0":  {Major: 1, Minor: 0},
		"2.13": {Major: 2, Minor: 13},
	}
	for want, v := range tests {
		if got := v.String(); got != want {
			t.Errorf("SchemaVersion{%d, %d}.String() = %q, want %q", v.Major, v.Minor, got, want)
		}
	}
}
