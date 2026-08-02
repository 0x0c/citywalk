// Package adminauth implements CW-0010 Unit 9's administrative half: "the administrative application
// programming interface (API) authenticates through the organization's identity provider and
// authorizes by role, with viewer, editor, and administrator as the initial set."
//
// The real external identity-provider integration — validating a token issued by whichever provider
// the organization actually runs (an OIDC issuer, a SAML identity provider, something else) —needs
// that provider's own configuration (issuer URL, JWKS endpoint, client id), which this repository has
// no access to and cannot invent. StaticKeyAuthenticator is this phase's stand-in Verifier for that
// gap: a fixed, operator-configured map of bearer credential to principal. Every caller in this
// package — the AdminService handler, the audit log — depends only on the Authenticator interface,
// so swapping StaticKeyAuthenticator for a real OIDC verifier later touches nothing here.
package adminauth

import (
	"context"
	"crypto/subtle"
	"errors"
)

// Role is one of the three roles CW-0010 Unit 9 names.
type Role string

const (
	RoleViewer        Role = "viewer"
	RoleEditor        Role = "editor"
	RoleAdministrator Role = "administrator"
)

// roleRank orders the three roles so Satisfies can answer "is this role at least that one" without
// every call site re-deriving the ordering by hand.
var roleRank = map[Role]int{
	RoleViewer:        1,
	RoleEditor:        2,
	RoleAdministrator: 3,
}

// Satisfies reports whether role is sufficient to meet a check that requires at least requirement —
// an administrator satisfies an editor requirement, an editor does not satisfy an administrator
// requirement. An unrecognized role satisfies nothing, including a viewer requirement: a role this
// package does not know about is a configuration bug, not an invitation to guess its intent.
func (role Role) Satisfies(requirement Role) bool {
	roleValue, ok := roleRank[role]
	if !ok {
		return false
	}
	return roleValue >= roleRank[requirement]
}

// Principal is the authenticated caller CW-0001 Unit 1's audit log records as "the actor" on every
// mutation.
type Principal struct {
	Subject string
	Role    Role
}

// ErrUnauthenticated is returned for a missing, unrecognized, or malformed credential.
var ErrUnauthenticated = errors.New("adminauth: invalid or missing credential")

// Authenticator verifies a bearer credential and returns the principal it belongs to.
type Authenticator interface {
	Authenticate(ctx context.Context, bearerToken string) (Principal, error)
}

// StaticKeyAuthenticator maps a fixed set of API keys to principals — see the package doc comment for
// why this exists instead of a real identity-provider client.
type StaticKeyAuthenticator struct {
	// Keys maps an API key to the principal presenting it. Populated from configuration
	// (internal/platform/config), never hard-coded.
	Keys map[string]Principal
}

// Authenticate looks up bearerToken among a.Keys. The comparison against each configured key runs in
// constant time per key (crypto/subtle), so no single comparison leaks how much of that one key
// matched; which key (if any) eventually matches is not itself secret; it is the one credential that
// legitimately authenticates.
func (a StaticKeyAuthenticator) Authenticate(_ context.Context, bearerToken string) (Principal, error) {
	if bearerToken == "" {
		return Principal{}, ErrUnauthenticated
	}
	for key, principal := range a.Keys {
		if len(key) != len(bearerToken) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(bearerToken)) == 1 {
			return principal, nil
		}
	}
	return Principal{}, ErrUnauthenticated
}
