// Package register implements FR-API-01 (device registration: "the SDK registers and updates a
// device and receives an identifier and a credential, sending attributes, locale, time zone,
// application version, and notification permission in the same call") and CW-0010 Unit 9's
// device-bound credential half.
package register

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/membership/ordinal"
	"github.com/0x0c/citywalk/internal/platform/devicetoken"
)

// AccessTokenTTL is how long an issued access token is valid. Short-lived by design (CW-0010 Unit
// 9): the SDK renews it against its longer-lived registration credential rather than the platform
// issuing tokens that stay valid for as long as the credential does.
const AccessTokenTTL = time.Hour

// ErrInvalidCredential is returned by RefreshToken for an unknown channel or a credential that
// doesn't match — deliberately the same error either way, so a caller probing for valid channel IDs
// learns nothing from which failure it got.
var ErrInvalidCredential = errors.New("register: invalid channel id or credential")

// Result is what a device holds after registering or refreshing: its channel identifier, an access
// token it can present immediately, and that token's expiry. Credential is set only by Register — a
// device receives it once, at registration, and RefreshToken never returns it again.
type Result struct {
	ChannelID   string
	Credential  string
	AccessToken string
	ExpiresAt   time.Time
}

// Register creates a new channel row for attrs — CW-0004's channel_field-sourced attributes (locale,
// time zone, application version, notification permission), written into the same attributes jsonb
// document every predicate reads, per migrations/0003_audience_channels.sql's own reasoning for why
// that column exists — and supportedSchemaMajor (CW-0003 Unit 4: the content schema major this
// device's SDK can render), allocates its membership ordinal (CW-0005's indexes are keyed by it, so a
// channel with none would never match a segment), and issues its first credential and access token.
func Register(
	ctx context.Context, pool *pgxpool.Pool, secret []byte,
	attrs map[string]any, supportedSchemaMajor int, now time.Time,
) (Result, error) {
	credential, err := devicetoken.NewCredential()
	if err != nil {
		return Result{}, fmt.Errorf("register: %w", err)
	}
	hash := devicetoken.HashCredential(credential)

	var channelID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO channels (attributes, credential_hash, supported_schema_major)
		VALUES ($1, $2, $3)
		RETURNING id
	`, attrs, hash, supportedSchemaMajor).Scan(&channelID); err != nil {
		return Result{}, fmt.Errorf("register: insert channel: %w", err)
	}

	if _, err := ordinal.Allocate(ctx, pool, channelID); err != nil {
		return Result{}, fmt.Errorf("register: %w", err)
	}

	accessToken, err := devicetoken.Issue(secret, channelID, now, AccessTokenTTL)
	if err != nil {
		return Result{}, fmt.Errorf("register: %w", err)
	}

	return Result{
		ChannelID: channelID, Credential: credential,
		AccessToken: accessToken, ExpiresAt: now.Add(AccessTokenTTL),
	}, nil
}

// RefreshToken verifies credential against channelID's stored hash and, on a match, issues a fresh
// access token. It fails closed (ErrInvalidCredential) for an unknown channel, a channel with no
// credential on file (one created outside Register — see migrations/0009_channel_auth.sql), or a
// credential that doesn't match.
func RefreshToken(
	ctx context.Context, pool *pgxpool.Pool, secret []byte, channelID, credential string, now time.Time,
) (Result, error) {
	var storedHash []byte
	err := pool.QueryRow(ctx, `SELECT credential_hash FROM channels WHERE id = $1`, channelID).Scan(&storedHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrInvalidCredential
		}
		return Result{}, fmt.Errorf("register: refresh: look up channel %s: %w", channelID, err)
	}
	if !devicetoken.CredentialMatches(storedHash, credential) {
		return Result{}, ErrInvalidCredential
	}

	accessToken, err := devicetoken.Issue(secret, channelID, now, AccessTokenTTL)
	if err != nil {
		return Result{}, fmt.Errorf("register: refresh: %w", err)
	}
	return Result{ChannelID: channelID, AccessToken: accessToken, ExpiresAt: now.Add(AccessTokenTTL)}, nil
}
