package config_test

import (
	"os"
	"testing"

	"github.com/0x0c/citywalk/internal/platform/config"
)

func TestLoadDefaultsListenAddr(t *testing.T) {
	for _, key := range []string{"CITYWALK_LISTEN_ADDR", "CITYWALK_POSTGRES_DSN", "CITYWALK_REDIS_ADDR"} {
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
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("CITYWALK_LISTEN_ADDR", ":9090")
	t.Setenv("CITYWALK_POSTGRES_DSN", "postgres://example/db")
	t.Setenv("CITYWALK_REDIS_ADDR", "localhost:6379")

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
}
