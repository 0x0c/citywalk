//go:build integration

// Run with: go test -tags=integration ./internal/channel/register/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package register_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/channel/register"
	"github.com/0x0c/citywalk/internal/platform/devicetoken"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

var testSecret = []byte("test-signing-secret-at-least-32-bytes-long")

func testPool(t *testing.T) *pgxpool.Pool {
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

func TestRegisterIssuesAVerifiableTokenAndAnOrdinal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	result, err := register.Register(ctx, pool, testSecret, map[string]any{"country": "JP"}, 1, now)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.ChannelID == "" {
		t.Fatal("Register: ChannelID is empty")
	}
	if result.Credential == "" {
		t.Fatal("Register: Credential is empty")
	}
	if result.AccessToken == "" {
		t.Fatal("Register: AccessToken is empty")
	}

	channelID, err := devicetoken.Verify(testSecret, result.AccessToken, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if channelID != result.ChannelID {
		t.Errorf("Verify() = %q, want %q", channelID, result.ChannelID)
	}

	var ordinal int64
	if err := pool.QueryRow(ctx, `SELECT ordinal FROM channel_ordinals WHERE channel_id = $1`, result.ChannelID).Scan(&ordinal); err != nil {
		t.Fatalf("query ordinal: %v", err)
	}

	var attrs map[string]any
	var supportedMajor int
	if err := pool.QueryRow(ctx, `SELECT attributes, supported_schema_major FROM channels WHERE id = $1`, result.ChannelID).Scan(&attrs, &supportedMajor); err != nil {
		t.Fatalf("query channel: %v", err)
	}
	if attrs["country"] != "JP" {
		t.Errorf("attributes[country] = %v, want JP", attrs["country"])
	}
	if supportedMajor != 1 {
		t.Errorf("supported_schema_major = %d, want 1", supportedMajor)
	}
}

func TestRefreshTokenIssuesANewTokenForAMatchingCredential(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	registered, err := register.Register(ctx, pool, testSecret, map[string]any{}, 1, now)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	later := now.Add(30 * time.Minute)
	refreshed, err := register.RefreshToken(ctx, pool, testSecret, registered.ChannelID, registered.Credential, later)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshed.AccessToken == registered.AccessToken {
		t.Error("RefreshToken() returned the same token as Register, want a freshly issued one")
	}

	channelID, err := devicetoken.Verify(testSecret, refreshed.AccessToken, later)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if channelID != registered.ChannelID {
		t.Errorf("Verify() = %q, want %q", channelID, registered.ChannelID)
	}
}

func TestRefreshTokenRejectsAWrongCredential(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	registered, err := register.Register(ctx, pool, testSecret, map[string]any{}, 1, now)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := register.RefreshToken(ctx, pool, testSecret, registered.ChannelID, "wrong-credential", now); err != register.ErrInvalidCredential {
		t.Errorf("RefreshToken() err = %v, want ErrInvalidCredential", err)
	}
}

func TestRefreshTokenRejectsAnUnknownChannel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := register.RefreshToken(ctx, pool, testSecret, "00000000-0000-0000-0000-000000000000", "any-credential", time.Now())
	if err != register.ErrInvalidCredential {
		t.Errorf("RefreshToken() err = %v, want ErrInvalidCredential", err)
	}
}

// TestRefreshTokenRejectsAChannelWithNoCredential demonstrates that a channel row created outside
// Register (every other package's test fixtures insert channels directly, with no credential_hash)
// can never authenticate — the migration's own reasoning for leaving that column nullable.
func TestRefreshTokenRejectsAChannelWithNoCredential(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	if _, err := register.RefreshToken(ctx, pool, testSecret, channelID, "any-credential", time.Now()); err != register.ErrInvalidCredential {
		t.Errorf("RefreshToken() err = %v, want ErrInvalidCredential", err)
	}
}
