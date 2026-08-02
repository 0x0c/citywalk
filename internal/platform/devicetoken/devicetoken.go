// Package devicetoken implements CW-0010 Unit 9's device-facing half: a short-lived token, signed by
// the platform, that binds a channel identifier. "Binding the channel to the token is what satisfies
// the requirement that naming another channel's identifier grants nothing" — a handler that trusts
// the channel identifier a verified token carries, rather than whatever a request body claims, cannot
// be made to act on a channel the caller does not hold a credential for.
//
// The token format is deliberately not JWT: a real JWT library accepts an "alg" field from the token
// itself and has to defend against alg-confusion attacks (a token claiming "alg: none", or claiming
// an asymmetric algorithm when the verifier only ever configured a symmetric one) as a result. This
// format has no algorithm field to attack — the verifier always computes an HMAC-SHA256 and compares
// it, full stop — which removes an entire vulnerability class by construction rather than by careful
// configuration.
package devicetoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrMalformed is returned for a token that isn't shaped like one this package issued.
	ErrMalformed = errors.New("devicetoken: malformed token")
	// ErrExpired is returned for a well-formed, correctly signed token whose validity window has
	// passed.
	ErrExpired = errors.New("devicetoken: expired")
	// ErrInvalidSignature is returned when the signature does not match — a forged or tampered
	// token, or one signed with a different secret (e.g. a stale signing key after rotation).
	ErrInvalidSignature = errors.New("devicetoken: invalid signature")
)

// claims is the signed payload: the channel identifier the token is bound to, and its validity
// window. It carries nothing else — no role, no scope — because a device token authenticates one
// channel and nothing more; CW-0010 Unit 9's separate identity-provider half handles roles.
type claims struct {
	ChannelID string `json:"channel_id"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// Issue mints a token binding channelID, valid from now until now+ttl, signed with secret. secret is
// the platform's token-signing key (see internal/platform/config); it must be the same key Verify is
// later called with, or every token this call issues will fail to verify.
func Issue(secret []byte, channelID string, now time.Time, ttl time.Duration) (string, error) {
	if channelID == "" {
		return "", fmt.Errorf("devicetoken: channel id is required")
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("devicetoken: signing secret is required")
	}

	c := claims{ChannelID: channelID, IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix()}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("devicetoken: marshal claims: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(secret, encodedPayload)
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks tokenString's signature against secret and its expiry against now, and returns the
// channel identifier it is bound to. The signature is checked before anything else about the token is
// trusted — including before its bytes are even parsed as claims — and the comparison uses
// hmac.Equal (constant-time) rather than ==, since a timing side channel on signature comparison is
// exactly the kind of thing that turns "signed" into "forgeable."
func Verify(secret []byte, tokenString string, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", fmt.Errorf("devicetoken: signing secret is required")
	}

	parts := strings.SplitN(tokenString, ".", 2)
	if len(parts) != 2 {
		return "", ErrMalformed
	}
	encodedPayload, encodedSig := parts[0], parts[1]

	gotSig, err := base64.RawURLEncoding.DecodeString(encodedSig)
	if err != nil {
		return "", ErrMalformed
	}
	wantSig := sign(secret, encodedPayload)
	if !hmac.Equal(wantSig, gotSig) {
		return "", ErrInvalidSignature
	}

	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return "", ErrMalformed
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", ErrMalformed
	}
	if c.ChannelID == "" {
		return "", ErrMalformed
	}
	if now.Unix() >= c.ExpiresAt {
		return "", ErrExpired
	}
	return c.ChannelID, nil
}

func sign(secret []byte, encodedPayload string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encodedPayload)) // hash.Hash.Write never returns an error.
	return mac.Sum(nil)
}

// credentialBytes is the entropy NewCredential generates — 256 bits, well past what's brute-forceable
// and matching the signing key's own security margin.
const credentialBytes = 32

// NewCredential generates a fresh, high-entropy registration credential — FR-API-01's "the SDK
// registers... and receives an identifier and a credential." The raw value is returned to the caller
// exactly once, at registration; only its hash (HashCredential) is ever persisted, the same reasoning
// a password is never stored in the clear.
func NewCredential() (string, error) {
	b := make([]byte, credentialBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("devicetoken: generate credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashCredential returns what gets persisted instead of the raw credential: a plain SHA-256 digest,
// not a MAC, since a generated 256-bit credential already has all the entropy a hash needs to resist
// brute force — the deliberately slow, salted schemes a low-entropy human password requires (bcrypt,
// scrypt, argon2) exist for a threat model this credential does not have.
func HashCredential(credential string) []byte {
	sum := sha256.Sum256([]byte(credential))
	return sum[:]
}

// CredentialMatches reports whether credential hashes to storedHash, in constant time — a
// registration-credential check is a security boundary the same way a password check is, and must
// not leak timing information about how much of the hash matched.
func CredentialMatches(storedHash []byte, credential string) bool {
	if len(storedHash) == 0 {
		return false
	}
	got := HashCredential(credential)
	return subtle.ConstantTimeCompare(storedHash, got) == 1
}
