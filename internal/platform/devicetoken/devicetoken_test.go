package devicetoken_test

import (
	"strings"
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/platform/devicetoken"
)

var testSecret = []byte("test-signing-secret-at-least-32-bytes-long")

func TestIssueThenVerifyRoundTrips(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	token, err := devicetoken.Issue(testSecret, "channel-1", now, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	channelID, err := devicetoken.Verify(testSecret, token, now.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if channelID != "channel-1" {
		t.Errorf("Verify() = %q, want %q", channelID, "channel-1")
	}
}

func TestVerifyRejectsAnExpiredToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	token, err := devicetoken.Issue(testSecret, "channel-1", now, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = devicetoken.Verify(testSecret, token, now.Add(time.Hour+time.Second))
	if err != devicetoken.ErrExpired {
		t.Errorf("Verify() err = %v, want ErrExpired", err)
	}
}

func TestVerifyRejectsATokenSignedWithADifferentSecret(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	token, err := devicetoken.Issue(testSecret, "channel-1", now, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = devicetoken.Verify([]byte("a-completely-different-secret-value"), token, now)
	if err != devicetoken.ErrInvalidSignature {
		t.Errorf("Verify() err = %v, want ErrInvalidSignature", err)
	}
}

// TestVerifyRejectsATamperedChannelID is the property CW-0010 Unit 9 exists for: a token minted for
// one channel must not verify as belonging to a different one, even after the payload is edited by
// hand — the signature must catch it.
func TestVerifyRejectsATamperedChannelID(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	token, err := devicetoken.Issue(testSecret, "channel-1", now, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("token %q does not have the expected two-part shape", token)
	}
	// Mint a token for a different channel and splice in the first token's signature: a forgery
	// attempt that must fail, not silently authenticate as channel-2.
	otherToken, err := devicetoken.Issue(testSecret, "channel-2", now, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	otherParts := strings.SplitN(otherToken, ".", 2)
	forged := otherParts[0] + "." + parts[1]

	if _, err := devicetoken.Verify(testSecret, forged, now); err != devicetoken.ErrInvalidSignature {
		t.Errorf("Verify(forged) err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []string{"", "not-a-token", "onlyonepart", "..", "a.b.c"}
	for _, tc := range cases {
		if _, err := devicetoken.Verify(testSecret, tc, now); err == nil {
			t.Errorf("Verify(%q) = nil error, want an error", tc)
		}
	}
}

func TestIssueRejectsAnEmptyChannelID(t *testing.T) {
	if _, err := devicetoken.Issue(testSecret, "", time.Now(), time.Hour); err == nil {
		t.Error("Issue(\"\") = nil error, want an error")
	}
}

func TestNewCredentialProducesDistinctHighEntropyValues(t *testing.T) {
	a, err := devicetoken.NewCredential()
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	b, err := devicetoken.NewCredential()
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if a == b {
		t.Error("NewCredential() returned the same value twice in a row, want distinct values")
	}
	if len(a) < 32 {
		t.Errorf("len(NewCredential()) = %d, want a substantially longer high-entropy value", len(a))
	}
}

func TestCredentialMatches(t *testing.T) {
	credential, err := devicetoken.NewCredential()
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	hash := devicetoken.HashCredential(credential)

	if !devicetoken.CredentialMatches(hash, credential) {
		t.Error("CredentialMatches(hash, credential) = false, want true")
	}
	if devicetoken.CredentialMatches(hash, "wrong-credential") {
		t.Error("CredentialMatches(hash, wrong) = true, want false")
	}
	if devicetoken.CredentialMatches(nil, credential) {
		t.Error("CredentialMatches(nil, credential) = true, want false (no stored hash can never match)")
	}
}
