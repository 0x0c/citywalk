//go:build integration

// Run with: go test -tags=integration ./internal/platform/eventlog/... with
// CITYWALK_TEST_KAFKA_BROKERS pointing at a scratch Kafka-compatible broker (Redpanda or Kafka
// itself — franz-go speaks the wire protocol, not anything broker-specific). Not reachable in every
// sandbox this repository's tests run in, hence the skip below rather than a hard failure.
package eventlog_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0x0c/citywalk/internal/platform/eventlog"
)

func testConfig(t *testing.T) eventlog.Config {
	t.Helper()
	raw := os.Getenv("CITYWALK_TEST_KAFKA_BROKERS")
	if raw == "" {
		t.Skip("CITYWALK_TEST_KAFKA_BROKERS must be set")
	}
	return eventlog.Config{
		Brokers: strings.Split(raw, ","),
		// A fresh topic per test run avoids one test's records confusing another's offset
		// expectations; the broker in a scratch integration environment is expected to allow
		// auto-creation, matching how the Postgres and Redis integration tests use a schema/instance
		// they alone own for the run.
		Topic: "citywalk.events.test." + uuid.NewString(),
	}
}

// TestProducerPublishAndConsumerPollRoundTrip is CW-0009 Unit 3's log-as-seam end to end: a record
// produced under a channel's partition key comes back out of a consumer group's Poll with the same
// key and value, and Commit advances the group's position so a second Poll against the same group
// sees nothing further to do.
func TestProducerPublishAndConsumerPollRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	producer, err := eventlog.NewProducer(cfg)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(producer.Close)
	if err := producer.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	key := eventlog.PartitionKey("channel-a")
	value := []byte(`{"id":"01912d2c-0000-7000-8000-000000000001"}`)
	if err := producer.Publish(ctx, key, value); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	consumer, err := eventlog.NewConsumer(cfg, "eventlog-test-roundtrip")
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	records, err := pollUntilNonEmpty(t, ctx, consumer)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Poll() = %d records, want 1", len(records))
	}
	if string(records[0].Key) != string(key) {
		t.Errorf("record key = %q, want %q", records[0].Key, key)
	}
	if string(records[0].Value) != string(value) {
		t.Errorf("record value = %q, want %q", records[0].Value, value)
	}

	if err := consumer.Commit(ctx, records); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// A fresh consumer joining the same group must resume past the committed record, not replay it —
	// CW-0009 Unit 3's "each consumer tracks its own position."
	resumed, err := eventlog.NewConsumer(cfg, "eventlog-test-roundtrip")
	if err != nil {
		t.Fatalf("NewConsumer (resumed): %v", err)
	}
	t.Cleanup(resumed.Close)

	pollCtx, pollCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pollCancel()
	again, err := resumed.Poll(pollCtx)
	if err != nil {
		t.Fatalf("Poll (resumed): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("Poll (resumed) = %d records, want 0 (already committed past the only record)", len(again))
	}
}

// TestPartitionKeyKeepsAChannelInOrder produces several records under the same channel's key,
// interleaved with a different channel's, and confirms a consumer reads the first channel's own
// records back in the order they were produced — CW-0009 Unit 3's "keeps one channel's events in
// order," exercised against a real broker's partition assignment rather than assumed from the
// key-hashing algorithm alone.
func TestPartitionKeyKeepsAChannelInOrder(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	producer, err := eventlog.NewProducer(cfg)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(producer.Close)

	keyA := eventlog.PartitionKey("channel-order-a")
	keyB := eventlog.PartitionKey("channel-order-b")
	sequence := []struct {
		key   []byte
		value string
	}{
		{keyA, "a-1"}, {keyB, "b-1"}, {keyA, "a-2"}, {keyB, "b-2"}, {keyA, "a-3"},
	}
	for _, s := range sequence {
		if err := producer.Publish(ctx, s.key, []byte(s.value)); err != nil {
			t.Fatalf("Publish(%s): %v", s.value, err)
		}
	}

	consumer, err := eventlog.NewConsumer(cfg, "eventlog-test-order")
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	var gotA []string
	deadline := time.Now().Add(20 * time.Second)
	for len(gotA) < 3 && time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
		records, err := consumer.Poll(pollCtx)
		pollCancel()
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		for _, r := range records {
			if string(r.Key) == string(keyA) {
				gotA = append(gotA, string(r.Value))
			}
		}
	}

	want := []string{"a-1", "a-2", "a-3"}
	if len(gotA) != len(want) {
		t.Fatalf("channel-order-a records = %v, want %v", gotA, want)
	}
	for i, v := range want {
		if gotA[i] != v {
			t.Errorf("channel-order-a record %d = %q, want %q (order not preserved)", i, gotA[i], v)
		}
	}
}

func pollUntilNonEmpty(t *testing.T, ctx context.Context, c *eventlog.Consumer) ([]eventlog.Record, error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		records, err := c.Poll(pollCtx)
		cancel()
		if err != nil {
			return nil, err
		}
		if len(records) > 0 {
			return records, nil
		}
	}
	return nil, nil
}
