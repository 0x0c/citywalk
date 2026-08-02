//go:build integration

// Run with: go test -tags=integration ./internal/event/... with CITYWALK_TEST_POSTGRES_DSN and
// CITYWALK_TEST_KAFKA_BROKERS both set — a scratch Postgres database (rollups still land there; only
// the log itself moves) and a scratch Kafka-compatible broker. Neither is guaranteed reachable in
// every environment these tests run in, hence the skip below rather than a hard failure.
package event_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/event/consumer"
	"github.com/0x0c/citywalk/internal/event/ingest"
	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/platform/eventlog"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

func logTestDeps(t *testing.T) (*pgxpool.Pool, eventlog.Config) {
	t.Helper()
	pgDSN := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	brokers := os.Getenv("CITYWALK_TEST_KAFKA_BROKERS")
	if pgDSN == "" || brokers == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_KAFKA_BROKERS must both be set")
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, pgDSN)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, table := range []string{"targeting_rollup", "campaign_rollup", "rollup_applied_events", "events_log", "messages", "channels"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}

	cfg := eventlog.Config{
		Brokers: strings.Split(brokers, ","),
		Topic:   "citywalk.events.test." + uuid.NewString(),
	}
	return pool, cfg
}

// TestLogPublisherAndRunOnceFromLogRoundTripThroughRollups is CW-0009 Unit 3's log-as-seam, exercised
// through the log-backed alternate path this pass adds: LogPublisher appends an event to a real
// Kafka-compatible broker, keyed by channel; RunOnceFromLog polls it back through a consumer group,
// applies it to the same rollup-writing logic RunOnce uses, and commits the group's offset. The
// targeting rollup a real caller would read must reflect the event exactly as if it had gone through
// the Postgres-backed path instead.
func TestLogPublisherAndRunOnceFromLogRoundTripThroughRollups(t *testing.T) {
	pool, cfg := logTestDeps(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	producer, err := eventlog.NewProducer(cfg)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(producer.Close)
	publisher := ingest.LogPublisher{Producer: producer}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		{ID: uuid.NewString(), ChannelID: channelID, Kind: model.KindCustom, Name: "screen_view", DeviceTime: now},
	}
	accepted, err := publisher.Publish(ctx, events, now)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("Publish() = %d, want 1", accepted)
	}

	logConsumer, err := eventlog.NewConsumer(cfg, consumer.LogRollupConsumerGroup)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(logConsumer.Close)

	var processed int
	deadline := time.Now().Add(20 * time.Second)
	for processed == 0 && time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := consumer.RunOnceFromLog(pollCtx, pool, logConsumer, nil)
		pollCancel()
		if err != nil {
			t.Fatalf("RunOnceFromLog: %v", err)
		}
		processed = n
	}
	if processed != 1 {
		t.Fatalf("RunOnceFromLog processed %d events across the poll window, want 1", processed)
	}

	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT count FROM targeting_rollup WHERE channel_id = $1 AND event_name = 'screen_view'`, channelID,
	).Scan(&count); err != nil {
		t.Fatalf("query targeting_rollup: %v", err)
	}
	if count != 1 {
		t.Errorf("targeting_rollup screen_view count = %d, want 1", count)
	}
}
