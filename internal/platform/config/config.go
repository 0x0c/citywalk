// Package config loads the process configuration from the environment. Every value that varies by
// deployment — a connection string, a listen address — lives here rather than hard-coded, per
// .agent-workflows/implement/workflow.md's no-secret-material guardrail.
package config

import (
	"fmt"
	"os"
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
}

// Load reads the configuration from environment variables, applying defaults for anything a phase-one
// deployment does not require to be set explicitly.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:  getenv("CITYWALK_LISTEN_ADDR", ":8080"),
		PostgresDSN: os.Getenv("CITYWALK_POSTGRES_DSN"),
		RedisAddr:   os.Getenv("CITYWALK_REDIS_ADDR"),
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("config: CITYWALK_LISTEN_ADDR must not be empty")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
