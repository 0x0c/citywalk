package config_test

import (
	"os"
	"testing"

	"github.com/0x0c/citywalk/internal/platform/adminauth"
	"github.com/0x0c/citywalk/internal/platform/config"
)

func TestLoadDefaultsListenAddr(t *testing.T) {
	for _, key := range []string{
		"CITYWALK_LISTEN_ADDR", "CITYWALK_POSTGRES_DSN", "CITYWALK_REDIS_ADDR",
		"CITYWALK_TOKEN_SIGNING_KEY", "CITYWALK_ADMIN_API_KEYS",
		"CITYWALK_CLICKHOUSE_DSN", "CITYWALK_CLICKHOUSE_MIRROR_ENABLED",
	} {
		v, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if err := os.Setenv(key, v); err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
		})
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.PostgresDSN != "" {
		t.Errorf("PostgresDSN = %q, want empty", cfg.PostgresDSN)
	}
	if cfg.RedisAddr != "" {
		t.Errorf("RedisAddr = %q, want empty", cfg.RedisAddr)
	}
	if len(cfg.TokenSigningKey) != 0 {
		t.Errorf("TokenSigningKey = %q, want empty", cfg.TokenSigningKey)
	}
	if len(cfg.AdminKeys) != 0 {
		t.Errorf("AdminKeys = %v, want empty", cfg.AdminKeys)
	}
	if cfg.ClickHouseDSN != "" {
		t.Errorf("ClickHouseDSN = %q, want empty", cfg.ClickHouseDSN)
	}
	// CW-0010 Unit 11: the flag defaults to the phase-one path, so an unset environment must leave
	// ClickHouse mirroring disabled.
	if cfg.ClickHouseMirrorEnabled {
		t.Error("ClickHouseMirrorEnabled = true, want false when CITYWALK_CLICKHOUSE_MIRROR_ENABLED is unset")
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("CITYWALK_LISTEN_ADDR", ":9090")
	t.Setenv("CITYWALK_POSTGRES_DSN", "postgres://example/db")
	t.Setenv("CITYWALK_REDIS_ADDR", "localhost:6379")
	t.Setenv("CITYWALK_TOKEN_SIGNING_KEY", "s3cr3t")
	t.Setenv("CITYWALK_ADMIN_API_KEYS", "abc123:alice:editor,def456:bob:viewer")
	t.Setenv("CITYWALK_CLICKHOUSE_DSN", "clickhouse://example/db")
	t.Setenv("CITYWALK_CLICKHOUSE_MIRROR_ENABLED", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if cfg.PostgresDSN != "postgres://example/db" {
		t.Errorf("PostgresDSN = %q, want %q", cfg.PostgresDSN, "postgres://example/db")
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want %q", cfg.RedisAddr, "localhost:6379")
	}
	if string(cfg.TokenSigningKey) != "s3cr3t" {
		t.Errorf("TokenSigningKey = %q, want %q", cfg.TokenSigningKey, "s3cr3t")
	}
	want := map[string]adminauth.Principal{
		"abc123": {Subject: "alice", Role: adminauth.RoleEditor},
		"def456": {Subject: "bob", Role: adminauth.RoleViewer},
	}
	if len(cfg.AdminKeys) != len(want) {
		t.Fatalf("AdminKeys = %v, want %v", cfg.AdminKeys, want)
	}
	for key, principal := range want {
		if got := cfg.AdminKeys[key]; got != principal {
			t.Errorf("AdminKeys[%q] = %+v, want %+v", key, got, principal)
		}
	}
	if cfg.ClickHouseDSN != "clickhouse://example/db" {
		t.Errorf("ClickHouseDSN = %q, want %q", cfg.ClickHouseDSN, "clickhouse://example/db")
	}
	if !cfg.ClickHouseMirrorEnabled {
		t.Error("ClickHouseMirrorEnabled = false, want true")
	}
}

func TestLoadRejectsAnUnparsableClickHouseMirrorEnabledValue(t *testing.T) {
	t.Setenv("CITYWALK_CLICKHOUSE_MIRROR_ENABLED", "not-a-bool")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load: got nil error, want one for an unparsable CITYWALK_CLICKHOUSE_MIRROR_ENABLED")
	}
}

func TestLoadRejectsAMalformedAdminKeyEntry(t *testing.T) {
	t.Setenv("CITYWALK_ADMIN_API_KEYS", "not-enough-parts")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load: got nil error, want one for a malformed admin key entry")
	}
}

func TestLoadRejectsAnUnrecognizedAdminRole(t *testing.T) {
	t.Setenv("CITYWALK_ADMIN_API_KEYS", "abc123:alice:superuser")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load: got nil error, want one for an unrecognized role")
	}
}
