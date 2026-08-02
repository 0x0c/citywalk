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

// CurrentMajor is the baseline Major version the server has always served. A major-version
// transition never changes this constant — it widens SupportedMajors instead, per CW-0003 Unit 4.
const CurrentMajor = 1

// SupportedMajors is the small, explicit set of schema Majors save-time validation admits today
// (CW-0003 Unit 4). It holds only CurrentMajor while no major-version transition is under way.
// Widening it to name the next Major too is what makes parallel emission possible in practice:
// content declaring the new Major can only be persisted, and so only ever reach payload.Build's
// SupportsMajor-driven selection, once its Major is listed here. Narrowing it back to one entry is
// the withdrawal Unit 4 describes, once the old Major's device share crosses the stated threshold.
// This stays a short, explicit list rather than an open range — a Major absent from it is refused
// exactly as before.
var SupportedMajors = []int{CurrentMajor}

// SupportsSchemaMajor reports whether major is one of SupportedMajors — the check save-time
// validation runs against a variant's declared SchemaVersion before persisting it.
func SupportsSchemaMajor(major int) bool {
	for _, m := range SupportedMajors {
		if m == major {
			return true
		}
	}
	return false
}

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
