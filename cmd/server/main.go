// Command server runs the phase-one single process (CW-0010 Unit 11): the definition, audience, and
// delivery services share this binary, one Postgres instance, and one Redis instance.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/internal/platform/adminauth"
	"github.com/0x0c/citywalk/internal/platform/config"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/observability"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/internal/platform/redisclient"
	"github.com/0x0c/citywalk/migrations"
)

func main() {
	logger := observability.NewLogger("citywalk-server")
	if err := run(logger); err != nil {
		logger.Error("server exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	providers, err := observability.Setup(ctx, "citywalk-server")
	if err != nil {
		return fmt.Errorf("set up observability: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := providers.Shutdown(shutdownCtx); err != nil {
			logger.Error("observability shutdown failed", slog.Any("error", err))
		}
	}()

	var pool *pgxpool.Pool
	if cfg.PostgresDSN != "" {
		pool, err = postgres.NewPool(ctx, cfg.PostgresDSN)
		if err != nil {
			return fmt.Errorf("connect to postgres: %w", err)
		}
		defer pool.Close()

		if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		logger.Info("postgres ready")
	} else {
		logger.Warn("CITYWALK_POSTGRES_DSN not set, running without postgres")
	}

	var redisClient *redis.Client
	if cfg.RedisAddr != "" {
		redisClient, err = redisclient.New(ctx, cfg.RedisAddr)
		if err != nil {
			return fmt.Errorf("connect to redis: %w", err)
		}
		defer func() {
			if err := redisClient.Close(); err != nil {
				logger.Error("redis close failed", slog.Any("error", err))
			}
		}()
		logger.Info("redis ready")
	} else {
		logger.Warn("CITYWALK_REDIS_ADDR not set, running without redis")
	}

	var adminAuthenticator adminauth.Authenticator
	if len(cfg.AdminKeys) > 0 {
		adminAuthenticator = adminauth.StaticKeyAuthenticator{Keys: cfg.AdminKeys}
	} else {
		logger.Warn("CITYWALK_ADMIN_API_KEYS not set, running without AdminService")
	}
	if len(cfg.TokenSigningKey) == 0 {
		logger.Warn("CITYWALK_TOKEN_SIGNING_KEY not set, running without ChannelService, DeliveryService, or EventService")
	}

	mux, err := connectserver.NewMux(pool, redisClient, cfg.TokenSigningKey, adminAuthenticator)
	if err != nil {
		return fmt.Errorf("build connect mux: %w", err)
	}

	// The mobile SDK and internal callers alike speak plain HTTP with Connect (CW-0010 Unit 2), so
	// the server accepts HTTP/2 over cleartext rather than requiring TLS termination in front of it.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		Protocols:         protocols,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", cfg.ListenAddr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down server: %w", err)
		}
		return <-serveErr
	case err := <-serveErr:
		return err
	}
}
