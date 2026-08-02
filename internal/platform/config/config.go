// Package config loads the process configuration from the environment. Every value that varies by
// deployment — a connection string, a listen address — lives here rather than hard-coded, per
// .agent-workflows/implement/workflow.md's no-secret-material guardrail.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/0x0c/citywalk/internal/platform/adminauth"
)

// EventPublisherPostgres and EventPublisherLog are EventPublisherMode's two values: CW-0010 Unit 11's
// staged-adoption switch between phase one's direct write to events_log and phase two's
// Kafka-compatible log (CW-0010 Unit 5). EventPublisherPostgres is the default Load applies when
// CITYWALK_EVENT_PUBLISHER is unset, since the log's cutover is a later operational decision this
// configuration flag makes possible, not one this pass throws by default.
const (
	EventPublisherPostgres = "postgres"
	EventPublisherLog      = "log"
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
	// ClickHouseDSN is the connection string for the measurement store CW-0010 Unit 6 names
	// (internal/platform/clickhouse). Empty leaves the ClickHouse mirroring job disabled regardless of
	// ClickHouseMirrorEnabled, the same "no DSN, no client" shape PostgresDSN and RedisAddr already
	// use.
	ClickHouseDSN string
	// ClickHouseMirrorEnabled selects between CW-0010 Unit 11's two phases for measurement storage.
	// false, the default, keeps the phase-one path exactly as it is today: events aggregated into the
	// Postgres rollups by internal/event/consumer, untouched by this flag. true additionally starts
	// internal/event/mirror's periodic job, which reads accepted events out of events_log and writes
	// them into ClickHouse — additive, not a replacement for the rollups devices' delivery depends on.
	// Flipping this is the operational decision Unit 11 reserves for once event volume justifies the
	// second phase; citywalk has no real production traffic yet, so it defaults to off.
	ClickHouseMirrorEnabled bool
	// EventPublisherMode selects where internal/event/ingest.Accept and .Record write an accepted
	// event batch: EventPublisherPostgres (the default) writes directly to events_log, exactly as
	// phase one always has; EventPublisherLog writes to the Kafka-compatible log instead
	// (internal/platform/eventlog, CW-0010 Unit 5), and additionally requires EventLogBrokers.
	EventPublisherMode string
	// EventLogBrokers is the Kafka-compatible log's seed broker addresses (CW-0010 Unit 5). Read and
	// required only when EventPublisherMode is EventPublisherLog.
	EventLogBrokers []string
	// EventLogTopic is the log's topic name. Read only when EventPublisherMode is EventPublisherLog.
	EventLogTopic string
}

// Load reads the configuration from environment variables, applying defaults for anything a phase-one
// deployment does not require to be set explicitly.
func Load() (Config, error) {
	adminKeys, err := parseAdminKeys(os.Getenv("CITYWALK_ADMIN_API_KEYS"))
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	clickHouseMirrorEnabled, err := parseBool("CITYWALK_CLICKHOUSE_MIRROR_ENABLED", os.Getenv("CITYWALK_CLICKHOUSE_MIRROR_ENABLED"))
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}

	cfg := Config{
		ListenAddr:              getenv("CITYWALK_LISTEN_ADDR", ":8080"),
		PostgresDSN:             os.Getenv("CITYWALK_POSTGRES_DSN"),
		RedisAddr:               os.Getenv("CITYWALK_REDIS_ADDR"),
		TokenSigningKey:         []byte(os.Getenv("CITYWALK_TOKEN_SIGNING_KEY")),
		AdminKeys:               adminKeys,
		ClickHouseDSN:           os.Getenv("CITYWALK_CLICKHOUSE_DSN"),
		ClickHouseMirrorEnabled: clickHouseMirrorEnabled,
		EventPublisherMode:      getenv("CITYWALK_EVENT_PUBLISHER", EventPublisherPostgres),
		EventLogBrokers:         parseEventLogBrokers(os.Getenv("CITYWALK_EVENT_LOG_BROKERS")),
		EventLogTopic:           getenv("CITYWALK_EVENT_LOG_TOPIC", "citywalk.events"),
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("config: CITYWALK_LISTEN_ADDR must not be empty")
	}
	switch cfg.EventPublisherMode {
	case EventPublisherPostgres:
		// No further requirement: phase one's default, always available.
	case EventPublisherLog:
		if len(cfg.EventLogBrokers) == 0 {
			return Config{}, fmt.Errorf(
				"config: CITYWALK_EVENT_PUBLISHER=%s requires CITYWALK_EVENT_LOG_BROKERS to be set", EventPublisherLog,
			)
		}
	default:
		return Config{}, fmt.Errorf(
			"config: CITYWALK_EVENT_PUBLISHER=%q is not %q or %q", cfg.EventPublisherMode, EventPublisherPostgres, EventPublisherLog,
		)
	}
	return cfg, nil
}

// parseBool reads an optional boolean environment variable, defaulting to false (CW-0010 Unit 11:
// "It defaults to the first phase's ... path") when raw is empty, so an unset flag is a valid
// phase-one deployment rather than a misconfiguration — the same "empty is a valid default" shape
// parseAdminKeys already uses. A non-empty value that strconv.ParseBool cannot read is an error, so a
// typo in the flag never silently behaves as either "on" or "off".
func parseBool(name, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean (true/false), got %q", name, raw)
	}
	return v, nil
}

// parseEventLogBrokers splits a comma-separated CITYWALK_EVENT_LOG_BROKERS into its addresses,
// trimming whitespace and dropping empty entries (a trailing comma, for instance) rather than passing
// a blank broker address through to eventlog.Config.
func parseEventLogBrokers(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var brokers []string
	for _, b := range strings.Split(raw, ",") {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		brokers = append(brokers, b)
	}
	return brokers
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
