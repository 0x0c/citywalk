// Package etag implements CW-0006 Unit 1: a content-derived entity tag over the elements that
// determine what a device would render, so the tag changes exactly when the rendered result would
// differ — never when an edit happens not to affect a given channel.
package etag

import (
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/0x0c/citywalk/internal/delivery/payload"
)

// Compute returns the entity tag for entries: a non-cryptographic 64-bit hash over each entry's
// identifier and version, its selected variant's identifier and schema version, and its governance
// policy's values. A cryptographic hash is deliberately not used — the tag guards against staleness
// between a server and a device that already trust each other, not against forgery.
//
// CW-0003 does not (yet) give ControlPolicy its own version counter the way Message has one, so the
// policy's field values are hashed directly rather than a version number standing in for them; the
// result changes exactly when the policy does, which is the property Unit 1 asks for either way.
//
// entries is sorted by MessageID before hashing, so the result does not depend on the order Build
// happened to return them in.
func Compute(entries []payload.Entry) string {
	sorted := make([]payload.Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].MessageID < sorted[j].MessageID })

	h := fnv.New64a()
	for _, e := range sorted {
		// hash.Hash.Write (which Fprintf calls into) is documented never to return an error.
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%d.%d\x00%d\x00%d\x00%t\x00%t\x00",
			e.MessageID, e.Version, e.VariantID, e.SchemaVersion.Major, e.SchemaVersion.Minor,
			e.ControlPolicy.PerMessageCap, int64(e.ControlPolicy.MinIntervalBetween),
			e.ControlPolicy.ExemptFromProjectCap, e.ControlPolicy.RequiresServerConfirmation,
		)
	}
	return fmt.Sprintf("%x", h.Sum64())
}
