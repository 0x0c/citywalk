package registry_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

func TestNormalizeSemVerOrdersNumericallyNotLexically(t *testing.T) {
	lower, err := registry.NormalizeSemVer("2.9.0")
	if err != nil {
		t.Fatalf("NormalizeSemVer(2.9.0): %v", err)
	}
	higher, err := registry.NormalizeSemVer("2.10.0")
	if err != nil {
		t.Fatalf("NormalizeSemVer(2.10.0): %v", err)
	}
	if lower >= higher {
		t.Errorf("NormalizeSemVer(2.9.0) = %q, NormalizeSemVer(2.10.0) = %q; want the former to sort lower", lower, higher)
	}
}

func TestNormalizeSemVerDefaultsMissingComponents(t *testing.T) {
	got, err := registry.NormalizeSemVer("2")
	if err != nil {
		t.Fatalf("NormalizeSemVer(2): %v", err)
	}
	want, err := registry.NormalizeSemVer("2.0.0")
	if err != nil {
		t.Fatalf("NormalizeSemVer(2.0.0): %v", err)
	}
	if got != want {
		t.Errorf("NormalizeSemVer(2) = %q, NormalizeSemVer(2.0.0) = %q; want them equal", got, want)
	}
}

func TestNormalizeSemVerRejectsTooManyComponents(t *testing.T) {
	if _, err := registry.NormalizeSemVer("1.2.3.4"); err == nil {
		t.Error("NormalizeSemVer(1.2.3.4): got nil error, want an error")
	}
}

func TestNormalizeSemVerRejectsNonNumericComponent(t *testing.T) {
	if _, err := registry.NormalizeSemVer("2.x.0"); err == nil {
		t.Error("NormalizeSemVer(2.x.0): got nil error, want an error")
	}
}
