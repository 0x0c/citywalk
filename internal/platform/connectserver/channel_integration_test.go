//go:build integration

// Run with: go test -tags=integration ./internal/platform/connectserver/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package connectserver_test

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	channelv1 "github.com/0x0c/citywalk/gen/citywalk/channel/v1"
	"github.com/0x0c/citywalk/gen/citywalk/channel/v1/channelv1connect"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/devicetoken"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

func channelTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool
}

// TestRegisterRPCIssuesACredentialAndAccessToken exercises FR-API-01 and CW-0010 Unit 9's
// device-bound credential half end to end over a real Connect RPC call: a fresh registration
// returns a channel id, a credential, and an immediately usable access token.
func TestRegisterRPCIssuesACredentialAndAccessToken(t *testing.T) {
	pool := channelTestPool(t)
	ctx := context.Background()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := channelv1connect.NewChannelServiceClient(server.Client(), server.URL)
	resp, err := client.Register(ctx, connect.NewRequest(&channelv1.RegisterRequest{
		AttributesJson:       []byte(`{"country":"JP"}`),
		SupportedSchemaMajor: 1,
	}))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.Msg.GetChannelId() == "" {
		t.Error("ChannelId is empty, want a generated channel id")
	}
	if resp.Msg.GetCredential() == "" {
		t.Error("Credential is empty, want a generated registration credential")
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Error("AccessToken is empty, want an immediately usable access token")
	}
}

// TestRefreshTokenRPCExchangesACredentialForAFreshAccessToken exercises RefreshToken end to end:
// a device presenting the credential Register issued receives a new access token, never the
// credential itself again.
func TestRefreshTokenRPCExchangesACredentialForAFreshAccessToken(t *testing.T) {
	pool := channelTestPool(t)
	ctx := context.Background()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := channelv1connect.NewChannelServiceClient(server.Client(), server.URL)
	registered, err := client.Register(ctx, connect.NewRequest(&channelv1.RegisterRequest{SupportedSchemaMajor: 1}))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	refreshed, err := client.RefreshToken(ctx, connect.NewRequest(&channelv1.RefreshTokenRequest{
		ChannelId:  registered.Msg.GetChannelId(),
		Credential: registered.Msg.GetCredential(),
	}))
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshed.Msg.GetAccessToken() == "" {
		t.Error("AccessToken is empty, want a fresh access token")
	}
	// The refreshed token isn't necessarily byte-different from the one Register issued — both can
	// carry the same channel id and the same second-granularity issued-at/expiry if the two calls
	// land in the same second, so equality alone proves nothing. What matters is that the refreshed
	// token verifies to the same channel — the property RefreshToken exists to preserve.
	verifiedChannelID, err := devicetoken.Verify(testTokenSecret, refreshed.Msg.GetAccessToken(), time.Now())
	if err != nil {
		t.Fatalf("devicetoken.Verify(refreshed token): %v", err)
	}
	if verifiedChannelID != registered.Msg.GetChannelId() {
		t.Errorf("refreshed token verifies to channel %q, want %q", verifiedChannelID, registered.Msg.GetChannelId())
	}
	if refreshed.Msg.GetAccessTokenExpiresAt() == nil {
		t.Error("AccessTokenExpiresAt is nil, want a set expiry")
	}
}

// TestRefreshTokenRPCRejectsAWrongCredential demonstrates the fail-closed contract over the wire:
// a credential that does not match the channel's stored hash is rejected as Unauthenticated, the
// same error an unknown channel id produces, so a caller learns nothing about which failed.
func TestRefreshTokenRPCRejectsAWrongCredential(t *testing.T) {
	pool := channelTestPool(t)
	ctx := context.Background()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := channelv1connect.NewChannelServiceClient(server.Client(), server.URL)
	registered, err := client.Register(ctx, connect.NewRequest(&channelv1.RegisterRequest{SupportedSchemaMajor: 1}))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err = client.RefreshToken(ctx, connect.NewRequest(&channelv1.RefreshTokenRequest{
		ChannelId:  registered.Msg.GetChannelId(),
		Credential: "wrong-credential",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("RefreshToken (wrong credential): err = %v, want CodeUnauthenticated", err)
	}
}

// TestRefreshTokenRPCRejectsAnUnknownChannel is the other half of the fail-closed contract: a
// channel id nobody registered produces the same error a wrong credential does.
func TestRefreshTokenRPCRejectsAnUnknownChannel(t *testing.T) {
	pool := channelTestPool(t)
	ctx := context.Background()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := channelv1connect.NewChannelServiceClient(server.Client(), server.URL)
	_, err = client.RefreshToken(ctx, connect.NewRequest(&channelv1.RefreshTokenRequest{
		ChannelId:  "00000000-0000-0000-0000-000000000000",
		Credential: "anything",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("RefreshToken (unknown channel): err = %v, want CodeUnauthenticated", err)
	}
}
