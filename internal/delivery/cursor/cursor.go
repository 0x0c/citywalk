// Package cursor implements CW-0006 Unit 3's version cursor: the opaque token a device echoes back
// on its next synchronization so the server can decide whether a delta is safe to serve, or whether
// to fall back to a full payload and issue a fresh one. Like Unit 1's entity tag, it is not
// cryptographically protected — it detects staleness and drift between a server and a device that
// already trust each other over an authenticated channel, not forgery, and a device that sends a
// forged cursor can only ever cause the server to fall back to a full payload (always safe) or to
// compute a delta against its own false premise (harms only that device, never another channel's
// data), so signing it would buy nothing.
package cursor

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cursor is everything the server needs to decide whether it can still honor a device's prior
// synchronization point.
type Cursor struct {
	// Seq is the change log sequence (internal/delivery/changelog) as of issuance: every message
	// with a change log row past this Seq changed after this cursor was handed out.
	Seq int64
	// MembershipHash is CW-0005's segment membership bitmap hash as of issuance — see
	// internal/membership/reverse.Hash. A mismatch against the channel's current hash means the
	// channel's own audience membership might have shifted since, which the change log (a message-
	// level record) cannot see, so Unit 3 always falls back to a full payload in that case rather
	// than risk missing an entry a device should have lost, or never received.
	MembershipHash string
	// IssuedAt is when this cursor was handed out: the reference point Unit 3 uses to detect a
	// message that became newly eligible purely by its delivery window opening (no change log row
	// is written for that — see internal/delivery/deliver's delta computation), and the point
	// checked against changelog.Retention to decide whether the log can still be trusted for
	// everything since.
	IssuedAt time.Time
}

// version prefixes every cursor this package encodes, so a cursor from an incompatible future
// format decodes as unrecognized (Decode returns ok=false) instead of being misinterpreted.
const version = "v1"

// Encode renders c as the opaque string CW-0006 Unit 3's wire cursor field carries.
func Encode(c Cursor) string {
	raw := fmt.Sprintf("%s:%d:%s:%d", version, c.Seq, c.MembershipHash, c.IssuedAt.UnixNano())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode parses a cursor a device sent back. ok is false for anything that does not parse as a
// well-formed cursor this package issued — an empty string, garbage, a corrupted value, or a
// different version — never an error: CW-0006 Unit 3 states that a cursor the server cannot honor
// falls back to a full payload rather than failing the request, and an undecodable cursor is the
// least honorable one there is.
func Decode(s string) (c Cursor, ok bool) {
	if s == "" {
		return Cursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, false
	}
	parts := strings.SplitN(string(raw), ":", 4)
	if len(parts) != 4 || parts[0] != version {
		return Cursor{}, false
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Cursor{}, false
	}
	nanos, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return Cursor{}, false
	}
	return Cursor{Seq: seq, MembershipHash: parts[2], IssuedAt: time.Unix(0, nanos).UTC()}, true
}
