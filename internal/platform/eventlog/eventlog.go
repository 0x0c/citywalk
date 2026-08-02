// Package eventlog wraps a franz-go producer and consumer against the Kafka-compatible log CW-0010
// Unit 5 names. Redpanda is the deployment target, but franz-go speaks the Kafka wire protocol
// rather than anything Redpanda-specific, so the same client here also works against a real Kafka
// broker — which is what this package's integration suite runs against, since no Redpanda instance
// is guaranteed reachable in every environment that can run the tests.
//
// Every record is keyed by channel identifier (CW-0009 Unit 3: "partitioned by channel identifier,
// which keeps one channel's events in order"). franz-go's default producer partitioner hashes a
// record's key the same way Kafka's own default partitioner does, so two events for the same channel
// always land on the same partition and are read back in the order they were produced.
//
// This package is not wired in by default. CW-0010 Unit 11 keeps phase one's direct write to
// events_log (internal/event/ingest.PostgresPublisher) as the active path; internal/platform/config
// selects this one only when explicitly configured to, and the cutover itself is left as a later
// operational decision. See internal/event/ingest.LogPublisher and
// internal/event/consumer.RunOnceFromLog for the domain-level glue built on top of this package.
package eventlog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Config is the Kafka-compatible log's connection settings: seed brokers and the one topic this
// package reads and writes. CW-0009's event envelope is the log's only payload, so one topic per
// deployment is enough — a consumer distinguishes channels by partition key (PartitionKey), not by
// topic.
type Config struct {
	Brokers []string
	Topic   string
}

func (c Config) validate() error {
	if len(c.Brokers) == 0 {
		return fmt.Errorf("eventlog: at least one broker is required")
	}
	for _, b := range c.Brokers {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("eventlog: broker address must not be blank")
		}
	}
	if strings.TrimSpace(c.Topic) == "" {
		return fmt.Errorf("eventlog: topic is required")
	}
	return nil
}

// PartitionKey returns the partition key CW-0009 Unit 3 requires: every event for channelID hashes to
// the same partition, which is what keeps one channel's events in order and lets consumers otherwise
// scale by partition. It is deterministic and channel identity alone, so a producer never needs to
// know how many partitions the topic has.
func PartitionKey(channelID string) []byte {
	return []byte(channelID)
}

// Producer publishes events to the log, one call per event, keyed by PartitionKey(event.ChannelID).
type Producer struct {
	client *kgo.Client
	topic  string
}

// NewProducer connects to cfg.Brokers and returns a Producer that appends to cfg.Topic with acks from
// every in-sync replica (durability over latency: NFR-REL-03 requires an accepted event to survive a
// downstream outage, and an unacknowledged write does not). It does not itself verify the broker is
// reachable — franz-go connects lazily on first use — so callers that want an early check should call
// Ping.
func NewProducer(cfg Config) (*Producer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.DefaultProduceTopic(cfg.Topic),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(kgo.SnappyCompression(), kgo.NoCompression()),
	)
	if err != nil {
		return nil, fmt.Errorf("eventlog: new producer client: %w", err)
	}
	return &Producer{client: client, topic: cfg.Topic}, nil
}

// Publish appends value to the log under key, blocking until the broker acknowledges the write or ctx
// ends.
func (p *Producer) Publish(ctx context.Context, key, value []byte) error {
	record := &kgo.Record{Topic: p.topic, Key: key, Value: value}
	result := p.client.ProduceSync(ctx, record)
	if err := result.FirstErr(); err != nil {
		return fmt.Errorf("eventlog: publish: %w", err)
	}
	return nil
}

// Ping verifies the broker is reachable.
func (p *Producer) Ping(ctx context.Context) error {
	if err := p.client.Ping(ctx); err != nil {
		return fmt.Errorf("eventlog: ping: %w", err)
	}
	return nil
}

// Close releases the underlying client. Publish is synchronous (ProduceSync), so nothing is left
// buffered by the time Close is called. Safe to call once, on shutdown.
func (p *Producer) Close() {
	p.client.Close()
}

// Record is one fetched message: its key and value, plus enough position metadata (opaque outside
// this package) for Consumer.Commit to advance the consumer group's offset past it.
type Record struct {
	Key   []byte
	Value []byte
	raw   *kgo.Record
}

// Consumer reads records from the log as part of a consumer group, tracking its position as that
// group's committed offsets (CW-0009 Unit 3: "Each consumer tracks its own position") rather than
// reinventing position tracking the way the Postgres-backed consumer's event_consumer_offsets table
// does.
type Consumer struct {
	client *kgo.Client
}

// NewConsumer connects to cfg.Brokers and joins groupID against cfg.Topic, with franz-go's autocommit
// disabled: this package's caller commits explicitly, after its own downstream writes commit, rather
// than on a timer — see Commit's doc comment for why that ordering is what makes replay-after-a-crash
// safe.
func NewConsumer(cfg Config, groupID string) (*Consumer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(groupID) == "" {
		return nil, fmt.Errorf("eventlog: consumer group id is required")
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.ConsumerGroup(groupID),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, fmt.Errorf("eventlog: new consumer client: %w", err)
	}
	return &Consumer{client: client}, nil
}

// Poll blocks until at least one record is available or ctx ends, and returns whatever was fetched
// (nil if ctx ended first) without committing. Call Commit once the caller has finished acting on
// every record in the batch.
func (c *Consumer) Poll(ctx context.Context) ([]Record, error) {
	fetches := c.client.PollFetches(ctx)

	// PollFetches injects a fake, topic-less fetch carrying ctx.Err() when ctx ends before any real
	// record arrives (its own doc comment: "these injected errors can be used to break out of a poll
	// loop"). That is this method's ordinary "nothing new right now" outcome, not a broker failure, so
	// it is filtered out here rather than surfaced as an error a caller (RunOnceFromLog) would treat as
	// something to fail loudly on.
	var realErrs []kgo.FetchError
	for _, e := range fetches.Errors() {
		if ctx.Err() != nil && errors.Is(e.Err, ctx.Err()) {
			continue
		}
		realErrs = append(realErrs, e)
	}
	if len(realErrs) > 0 {
		return nil, fmt.Errorf("eventlog: poll: %s", formatFetchErrors(realErrs))
	}

	raw := fetches.Records()
	records := make([]Record, len(raw))
	for i, r := range raw {
		records[i] = Record{Key: r.Key, Value: r.Value, raw: r}
	}
	return records, nil
}

// Commit advances this consumer group's committed offsets past every record in batch. Call it only
// after whatever the caller does with batch has itself durably committed: CW-0009 Unit 3 requires
// at-least-once delivery, which this ordering (act, then commit position) provides — a crash between
// the two leaves the group's offset behind, so the batch is refetched and reprocessed, never skipped.
func (c *Consumer) Commit(ctx context.Context, batch []Record) error {
	if len(batch) == 0 {
		return nil
	}
	raw := make([]*kgo.Record, len(batch))
	for i, r := range batch {
		raw[i] = r.raw
	}
	if err := c.client.CommitRecords(ctx, raw...); err != nil {
		return fmt.Errorf("eventlog: commit offsets: %w", err)
	}
	return nil
}

// Close releases the underlying client, leaving the consumer group so a rebalance is not left waiting
// on this member's session timeout. Safe to call once, on shutdown.
func (c *Consumer) Close() {
	c.client.Close()
}

func formatFetchErrors(errs []kgo.FetchError) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = fmt.Sprintf("%s[%d]: %v", e.Topic, e.Partition, e.Err)
	}
	return strings.Join(parts, "; ")
}
