package cursor_test

import (
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/delivery/cursor"
)

func TestEncodeDecodeRoundTrips(t *testing.T) {
	c := cursor.Cursor{
		Seq: 42, MembershipHash: "abc123",
		IssuedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	encoded := cursor.Encode(c)
	if encoded == "" {
		t.Fatal("Encode returned an empty string")
	}

	decoded, ok := cursor.Decode(encoded)
	if !ok {
		t.Fatal("Decode ok = false, want true for a cursor this package just encoded")
	}
	if decoded.Seq != c.Seq {
		t.Errorf("Seq = %d, want %d", decoded.Seq, c.Seq)
	}
	if decoded.MembershipHash != c.MembershipHash {
		t.Errorf("MembershipHash = %q, want %q", decoded.MembershipHash, c.MembershipHash)
	}
	if !decoded.IssuedAt.Equal(c.IssuedAt) {
		t.Errorf("IssuedAt = %v, want %v", decoded.IssuedAt, c.IssuedAt)
	}
}

// TestDecodeRejectsGarbageWithoutError demonstrates CW-0006 Unit 3's stated contract: an undecodable
// cursor is unrecognized, never an error — the caller (internal/delivery/deliver) falls back to a
// full payload instead of failing the request.
func TestDecodeRejectsGarbageWithoutError(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-cursor-at-all",
		"not base64 at all !!!",
		"dW5rbm93bi1mb3JtYXQ", // valid base64, wrong internal shape
	} {
		if _, ok := cursor.Decode(s); ok {
			t.Errorf("Decode(%q) ok = true, want false", s)
		}
	}
}

func TestDecodeRejectsAnEmptyString(t *testing.T) {
	if _, ok := cursor.Decode(""); ok {
		t.Error("Decode(\"\") ok = true, want false")
	}
}
