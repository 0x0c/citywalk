// Package config loads the process configuration from the environment. Every value that varies by
// deployment — a connection string, a listen address — lives here rather than hard-coded, per
// .agent-workflows/implement/workflow.md's no-secret-material guardrail.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/0x0c/citywalk/internal/platform/adminauth"
)

// Config is the phase-one single process configuration (CW-0010 Unit 11): the definition, audience,
// and delivery services run together against one PostgreSQL and one Redis instance.
type Config struct {
	// ListenAddr is the address the Connect HTTP server binds to.
	ListenAddr string
	// PostgresDSN is the connection string for the durable record store (CW-0010 Unit 3). Empty
	// disables Postgres-backed features, so the server can still boot for local development.
	PostgresDSN string
	// RedisAddr is the address of the Redis instance backing hot lookups and counters (CW-0010 Unit
	// 4). Empty disables Redis-backed features.
	RedisAddr string
	// TokenSigningKey signs and verifies CW-0010 Unit 9's device tokens (internal/platform/devicetoken).
	// Empty disables ChannelService, DeliveryService, and EventService, since none of them can operate
	// without a signing key to issue or verify tokens against.
	TokenSigningKey []byte
	// AdminKeys maps an administrative API key to the principal presenting it — CW-0010 Unit 9's
	// StaticKeyAuthenticator stand-in for a real identity-provider integration (see
	// internal/platform/adminauth's package doc comment for why). Empty disables AdminService.
	AdminKeys map[string]adminauth.Principal
}

// Load reads the configuration from environment variables, applying defaults for anything a phase-one
// deployment does not require to be set explicitly.
func Load() (Config, error) {
	adminKeys, err := parseAdminKeys(os.Getenv("CITYWALK_ADMIN_API_KEYS"))
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}

	cfg := Config{
		ListenAddr:      getenv("CITYWALK_LISTEN_ADDR", ":8080"),
		PostgresDSN:     os.Getenv("CITYWALK_POSTGRES_DSN"),
		RedisAddr:       os.Getenv("CITYWALK_REDIS_ADDR"),
		TokenSigningKey: []byte(os.Getenv("CITYWALK_TOKEN_SIGNING_KEY")),
		AdminKeys:       adminKeys,
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("config: CITYWALK_LISTEN_ADDR must not be empty")
	}
	return cfg, nil
}

// parseAdminKeys decodes CITYWALK_ADMIN_API_KEYS: a comma-separated list of
// "key:subject:role" triples, e.g. "abc123:alice:editor,def456:bob:viewer". An empty input yields a
// nil map (AdminService stays disabled) rather than an error, since a phase-one deployment with no
// administrative access configured yet is a valid state, not a misconfiguration.
func parseAdminKeys(raw string) (map[string]adminauth.Principal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	keys := make(map[string]adminauth.Principal)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("CITYWALK_ADMIN_API_KEYS entry %q must be key:subject:role", entry)
		}
		key, subject, role := parts[0], parts[1], adminauth.Role(parts[2])
		if key == "" || subject == "" {
			return nil, fmt.Errorf("CITYWALK_ADMIN_API_KEYS entry %q must have a non-empty key and subject", entry)
		}
		if !role.Satisfies(adminauth.RoleViewer) {
			return nil, fmt.Errorf("CITYWALK_ADMIN_API_KEYS entry %q names unrecognized role %q", entry, parts[2])
		}
		keys[key] = adminauth.Principal{Subject: subject, Role: role}
	}
	return keys, nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
