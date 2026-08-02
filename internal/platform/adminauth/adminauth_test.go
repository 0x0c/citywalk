package adminauth_test

import (
	"context"
	"testing"

	"github.com/0x0c/citywalk/internal/platform/adminauth"
)

func TestRoleSatisfies(t *testing.T) {
	cases := []struct {
		role        adminauth.Role
		requirement adminauth.Role
		want        bool
	}{
		{adminauth.RoleAdministrator, adminauth.RoleViewer, true},
		{adminauth.RoleAdministrator, adminauth.RoleEditor, true},
		{adminauth.RoleAdministrator, adminauth.RoleAdministrator, true},
		{adminauth.RoleEditor, adminauth.RoleViewer, true},
		{adminauth.RoleEditor, adminauth.RoleEditor, true},
		{adminauth.RoleEditor, adminauth.RoleAdministrator, false},
		{adminauth.RoleViewer, adminauth.RoleEditor, false},
		{adminauth.RoleViewer, adminauth.RoleViewer, true},
		{adminauth.Role("bogus"), adminauth.RoleViewer, false},
	}
	for _, tc := range cases {
		if got := tc.role.Satisfies(tc.requirement); got != tc.want {
			t.Errorf("Role(%q).Satisfies(%q) = %v, want %v", tc.role, tc.requirement, got, tc.want)
		}
	}
}

func TestStaticKeyAuthenticatorAuthenticatesAKnownKey(t *testing.T) {
	auth := adminauth.StaticKeyAuthenticator{
		Keys: map[string]adminauth.Principal{
			"key-for-alice": {Subject: "alice", Role: adminauth.RoleAdministrator},
		},
	}
	p, err := auth.Authenticate(context.Background(), "key-for-alice")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Subject != "alice" || p.Role != adminauth.RoleAdministrator {
		t.Errorf("Authenticate() = %+v, want alice/administrator", p)
	}
}

func TestStaticKeyAuthenticatorRejectsAnUnknownKey(t *testing.T) {
	auth := adminauth.StaticKeyAuthenticator{
		Keys: map[string]adminauth.Principal{"key-for-alice": {Subject: "alice", Role: adminauth.RoleViewer}},
	}
	if _, err := auth.Authenticate(context.Background(), "not-a-real-key"); err != adminauth.ErrUnauthenticated {
		t.Errorf("Authenticate() err = %v, want ErrUnauthenticated", err)
	}
}

func TestStaticKeyAuthenticatorRejectsAnEmptyToken(t *testing.T) {
	auth := adminauth.StaticKeyAuthenticator{Keys: map[string]adminauth.Principal{}}
	if _, err := auth.Authenticate(context.Background(), ""); err != adminauth.ErrUnauthenticated {
		t.Errorf("Authenticate(\"\") err = %v, want ErrUnauthenticated", err)
	}
}
