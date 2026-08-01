package registry

import (
	"fmt"
	"strconv"
	"strings"
)

// semVerFieldWidth is how many digits each dot-separated component is zero-padded to. 10 digits
// covers any component up to 9,999,999,999, far beyond any real version number, while staying a
// fixed width so the padded string sorts identically to numeric order.
const semVerFieldWidth = 10

// NormalizeSemVer rewrites a semantic version string into a form that sorts correctly under plain
// lexical (byte-wise) comparison: each of the up-to-three dot-separated numeric components is
// zero-padded to semVerFieldWidth digits. "2.9.0" becomes "0000000002.0000000009.0000000000", which
// now correctly sorts below "2.10.0"'s "0000000002.0000000010.0000000000" — the ordering CW-0004
// Unit 1 requires and plain string comparison gets backwards.
//
// A missing minor or patch component defaults to 0, so "2" and "2.0.0" normalize identically.
func NormalizeSemVer(s string) (string, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return "", fmt.Errorf("registry: %q is not a semantic version (more than three components)", s)
	}

	padded := make([]string, 3)
	for i := range padded {
		component := "0"
		if i < len(parts) {
			component = parts[i]
		}
		n, err := strconv.ParseUint(component, 10, 64)
		if err != nil {
			return "", fmt.Errorf("registry: %q is not a semantic version: component %q is not a non-negative integer", s, component)
		}
		padded[i] = fmt.Sprintf("%0*d", semVerFieldWidth, n)
	}
	return strings.Join(padded, "."), nil
}
