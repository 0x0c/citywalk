// Package model holds the message definition entities CW-0003 designs: the campaign-side Message,
// its Variants, Triggers, DisplayConditions, and ControlPolicy, plus the Content tagged union each
// Variant carries. The audience service (CW-0004, CW-0005) and the delivery service (CW-0002,
// CW-0006) both read these types; the definition service that validates and persists them is
// CW-0001 Unit 1's own pass.
package model

import "fmt"

// SchemaVersion identifies the shape of a Content document (CW-0003 Unit 4). Within a Major version,
// changes are additive only, so an SDK that meets a higher Minor than it knows can still render by
// ignoring the fields it does not recognize; a different Major is a breaking change that requires
// parallel emission rather than in-place migration.
type SchemaVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

// CurrentMajor is the only Major version the server emits and accepts today. A second Major arrives
// as parallel emission (CW-0003 Unit 4), not as a change to this constant.
const CurrentMajor = 1

// SupportsMajor reports whether a device that declared support for declaredMajor can render content
// carrying this version's Major — the check the delivery service runs per CW-0003 Unit 4's parallel
// emission rule. Minor is irrelevant to the check: a lower Minor within the same Major is always
// renderable, since Minor changes are additive only.
func (v SchemaVersion) SupportsMajor(declaredMajor int) bool {
	return v.Major == declaredMajor
}

func (v SchemaVersion) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}
