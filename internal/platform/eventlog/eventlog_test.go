package eventlog_test

import (
	"testing"

	"github.com/0x0c/citywalk/internal/platform/eventlog"
)

// TestPartitionKeyIsDeterministicAndDistinguishesChannels is CW-0009 Unit 3's partitioning
// requirement at the unit that does not need a broker: the same channel identifier always produces
// the same key (so franz-go's key-hashing partitioner always routes it to the same partition, keeping
// one channel's events in order), and different channels produce different keys (so the log can
// actually spread work across partitions instead of every channel colliding on one).
func TestPartitionKeyIsDeterministicAndDistinguishesChannels(t *testing.T) {
	a1 := eventlog.PartitionKey("channel-a")
	a2 := eventlog.PartitionKey("channel-a")
	b := eventlog.PartitionKey("channel-b")

	if string(a1) != string(a2) {
		t.Errorf("PartitionKey(%q) = %q and %q, want equal on repeated calls", "channel-a", a1, a2)
	}
	if string(a1) == string(b) {
		t.Errorf("PartitionKey(%q) and PartitionKey(%q) both = %q, want distinct keys for distinct channels", "channel-a", "channel-b", a1)
	}
}

// TestNewProducerRejectsAnEmptyConfig confirms Config's validation, the config-selection surface this
// package exposes without needing a live broker: an empty broker list or topic is a misconfiguration
// caught at construction, not a connection failure discovered later.
func TestNewProducerRejectsAnEmptyConfig(t *testing.T) {
	tests := map[string]eventlog.Config{
		"no brokers":   {Brokers: nil, Topic: "citywalk.events"},
		"blank broker": {Brokers: []string{" "}, Topic: "citywalk.events"},
		"no topic":     {Brokers: []string{"localhost:9092"}, Topic: ""},
		"blank topic":  {Brokers: []string{"localhost:9092"}, Topic: "  "},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := eventlog.NewProducer(cfg); err == nil {
				t.Errorf("NewProducer(%+v): got nil error, want one", cfg)
			}
		})
	}
}

// TestNewConsumerRejectsAnEmptyConfigOrGroupID mirrors TestNewProducerRejectsAnEmptyConfig for the
// consumer side, plus the consumer's own extra required field: a non-empty group id, since that is
// what CW-0009 Unit 3's "each consumer tracks its own position" maps onto for a Kafka-compatible log.
func TestNewConsumerRejectsAnEmptyConfigOrGroupID(t *testing.T) {
	validCfg := eventlog.Config{Brokers: []string{"localhost:9092"}, Topic: "citywalk.events"}

	if _, err := eventlog.NewConsumer(eventlog.Config{}, "rollup"); err == nil {
		t.Error("NewConsumer with empty config: got nil error, want one")
	}
	if _, err := eventlog.NewConsumer(validCfg, ""); err == nil {
		t.Error("NewConsumer with empty group id: got nil error, want one")
	}
	if _, err := eventlog.NewConsumer(validCfg, "   "); err == nil {
		t.Error("NewConsumer with blank group id: got nil error, want one")
	}
}
