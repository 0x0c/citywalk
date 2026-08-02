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
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/audience/registry"
	"github.com/0x0c/citywalk/internal/event/consumer"
	"github.com/0x0c/citywalk/internal/event/ingest"
	"github.com/0x0c/citywalk/internal/event/mirror"
	"github.com/0x0c/citywalk/internal/governance/budget"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/platform/adminauth"
	"github.com/0x0c/citywalk/internal/platform/clickhouse"
	"github.com/0x0c/citywalk/internal/platform/config"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/eventlog"
	"github.com/0x0c/citywalk/internal/platform/jobqueue"
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

	if pool != nil {
		// CW-0010 Unit 8: the job queue backs the platform's periodic jobs. CW-0005 Unit 6's
		// membership reconciliation and CW-0010 Unit 8's own activation-slot proof only need pool;
		// CW-0009 Unit 1's rollup recompute additionally records impressions against the project
		// budget when Redis is configured, mirroring how connectserver's EventServer treats Redis as
		// optional.
		//
		// registry.New() with no definitions is a placeholder: CW-0004's attribute registry has no
		// production construction path yet (no attribute definition is sourced from anywhere outside
		// a test fixture), and neither does segment creation (no AdminService RPC creates one), so a
		// fresh deployment always reconciles zero segments regardless of what the registry contains.
		// Wiring a real registry here is CW-0004's prerequisite work, not this pass's.
		reg, err := registry.New()
		if err != nil {
			return fmt.Errorf("build placeholder attribute registry: %w", err)
		}

		var budgetCounter *budget.Counter
		if redisClient != nil {
			budgetCounter = &budget.Counter{Redis: redisClient, Window: 24 * time.Hour}
		}

		workers := river.NewWorkers()
		river.AddWorker(workers, &batch.ReconcileWorker{Pool: pool, Redis: redisClient, Registry: reg})
		river.AddWorker(workers, &consumer.RollupRecomputeWorker{Pool: pool, BudgetCounter: budgetCounter})
		river.AddWorker(workers, &jobqueue.ActivationSlotWorker{Logger: logger})

		periodicJobs := []*river.PeriodicJob{
			batch.ReconcilePeriodicJob(),
			consumer.RollupRecomputePeriodicJob(),
			jobqueue.ActivationSlotPeriodicJob(),
		}

		// CW-0010 Unit 11: the ClickHouse mirroring job is the second phase's measurement path,
		// config-gated and off by default. Nothing here runs, and no ClickHouse connection is even
		// opened, unless an operator sets both the flag and the DSN — see internal/platform/config's
		// ClickHouseMirrorEnabled doc comment for why this stays additive to, not a replacement for,
		// the rollup path just registered above.
		if cfg.ClickHouseMirrorEnabled {
			if cfg.ClickHouseDSN != "" {
				chConn, err := clickhouse.New(ctx, cfg.ClickHouseDSN)
				if err != nil {
					return fmt.Errorf("connect to clickhouse: %w", err)
				}
				defer func() {
					if err := chConn.Close(); err != nil {
						logger.Error("clickhouse close failed", slog.Any("error", err))
					}
				}()
				logger.Info("clickhouse ready")

				chClient := &clickhouse.Client{Conn: chConn}
				river.AddWorker(workers, &mirror.MirrorWorker{Pool: pool, Client: chClient})
				periodicJobs = append(periodicJobs, mirror.MirrorPeriodicJob())
			} else {
				logger.Warn("CITYWALK_CLICKHOUSE_MIRROR_ENABLED is true but CITYWALK_CLICKHOUSE_DSN is not set, running without the ClickHouse mirror (CW-0010 Unit 6)")
			}
		}

		jobClient, err := jobqueue.New(pool, workers, periodicJobs, logger)
		if err != nil {
			return fmt.Errorf("build job queue client: %w", err)
		}
		if err := jobClient.Start(ctx); err != nil {
			return fmt.Errorf("start job queue client: %w", err)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := jobClient.Stop(shutdownCtx); err != nil {
				logger.Error("job queue client stop failed", slog.Any("error", err))
			}
		}()
		logger.Info("job queue ready")
	} else {
		logger.Warn("CITYWALK_POSTGRES_DSN not set, running without the job queue (CW-0010 Unit 8)")
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

	// eventPublisher is CW-0010 Unit 11's staged-adoption seam: a nil value here leaves NewMux to
	// default to the phase-one Postgres path (ingest.PostgresPublisher), which is what
	// CITYWALK_EVENT_PUBLISHER defaults to as well. The log path is constructed only when explicitly
	// selected, since connecting to a broker that is not actually there would otherwise fail a
	// deployment that never asked for it.
	var eventPublisher ingest.Publisher
	if cfg.EventPublisherMode == config.EventPublisherLog {
		logProducer, err := eventlog.NewProducer(eventlog.Config{Brokers: cfg.EventLogBrokers, Topic: cfg.EventLogTopic})
		if err != nil {
			return fmt.Errorf("connect to event log: %w", err)
		}
		defer logProducer.Close()
		eventPublisher = ingest.LogPublisher{Producer: logProducer}
		logger.Info("event publisher: kafka-compatible log", slog.Any("brokers", cfg.EventLogBrokers), slog.String("topic", cfg.EventLogTopic))
	} else {
		logger.Info("event publisher: postgres (phase one default)")
	}

	mux, err := connectserver.NewMux(pool, redisClient, cfg.TokenSigningKey, adminAuthenticator, eventPublisher)
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
