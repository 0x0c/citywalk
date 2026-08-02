package connectserver

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	platformv1 "github.com/0x0c/citywalk/gen/citywalk/platform/v1"
	"github.com/0x0c/citywalk/internal/platform/adminauth"
)

// TestChannelIDFromContextRoundTrip is the mechanism CW-0010 Unit 9's "the identifier in the request
// is ignored in favor of the one in the token" rests on: a handler reads the channel it acts on from
// here, so this carrier has to be the only way one gets in.
func TestChannelIDFromContextRoundTrip(t *testing.T) {
	ctx := withChannelID(context.Background(), "channel-1")

	got, ok := channelIDFromContext(ctx)
	if !ok || got != "channel-1" {
		t.Errorf("channelIDFromContext = (%q, %v), want (\"channel-1\", true)", got, ok)
	}
}

// TestChannelIDFromContextReportsAnUnauthenticatedContext keeps a handler from treating the zero
// string as a real channel: without the "not present" signal, a request that skipped the interceptor
// would read as a request for the channel whose identifier is "".
func TestChannelIDFromContextReportsAnUnauthenticatedContext(t *testing.T) {
	got, ok := channelIDFromContext(context.Background())
	if ok {
		t.Errorf("channelIDFromContext on a bare context = (%q, true), want ok=false", got)
	}
}

func TestPrincipalFromContextRoundTrip(t *testing.T) {
	want := adminauth.Principal{Subject: "operator-1", Role: adminauth.RoleAdministrator}
	ctx := withPrincipal(context.Background(), want)

	got, ok := principalFromContext(ctx)
	if !ok {
		t.Fatal("principalFromContext: got ok=false, want the stored principal")
	}
	if got != want {
		t.Errorf("principalFromContext = %+v, want %+v", got, want)
	}
}

func TestPrincipalFromContextReportsAnUnauthenticatedContext(t *testing.T) {
	if _, ok := principalFromContext(context.Background()); ok {
		t.Error("principalFromContext on a bare context: got ok=true, want false")
	}
}

// TestBearerTokenStripsTheScheme covers the shape clients actually send. The no-header case has to
// come back empty rather than as some partial string, because an empty token is exactly what
// deviceAuthInterceptor turns into an unauthenticated rejection.
func TestBearerTokenStripsTheScheme(t *testing.T) {
	tests := map[string]string{
		"Bearer abc.def.ghi": "abc.def.ghi",
		"":                   "",
	}

	for header, want := range tests {
		req := connect.NewRequest(&platformv1.CheckRequest{})
		if header != "" {
			req.Header().Set("Authorization", header)
		}
		if got := bearerToken(req); got != want {
			t.Errorf("bearerToken(Authorization: %q) = %q, want %q", header, got, want)
		}
	}
}

// TestBearerTokenLeavesAnUnprefixedCredentialIntact records the deliberate leniency here: a header
// without the scheme is passed through as the credential rather than discarded, so verification —
// not this parser — is what decides whether it is valid.
func TestBearerTokenLeavesAnUnprefixedCredentialIntact(t *testing.T) {
	req := connect.NewRequest(&platformv1.CheckRequest{})
	req.Header().Set("Authorization", "abc.def.ghi")

	if got := bearerToken(req); got != "abc.def.ghi" {
		t.Errorf("bearerToken = %q, want the raw credential passed through", got)
	}
}
