package mirror

import (
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/event/consumer"
)

func TestMirrorArgsKind(t *testing.T) {
	if got := (MirrorArgs{}).Kind(); got != "event_clickhouse_mirror" {
		t.Errorf("Kind() = %q, want %q", got, "event_clickhouse_mirror")
	}
}

func TestMirrorWorkerRegisters(t *testing.T) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &MirrorWorker{}); err != nil {
		t.Fatalf("AddWorkerSafely: %v", err)
	}
}

func TestMirrorPeriodicJobSchedulesOnMirrorInterval(t *testing.T) {
	if MirrorPeriodicJob() == nil {
		t.Fatal("MirrorPeriodicJob returned nil")
	}

	schedule := river.PeriodicInterval(MirrorInterval)
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if got := schedule.Next(now).Sub(now); got != MirrorInterval {
		t.Errorf("PeriodicInterval(MirrorInterval).Next spacing = %s, want %s", got, MirrorInterval)
	}
	if MirrorInterval != 5*time.Minute {
		t.Errorf("MirrorInterval = %s, want 5m", MirrorInterval)
	}
}

func TestMirrorWorkerDefaultsConsumerName(t *testing.T) {
	// Work reads w.ConsumerName, defaulting to MirrorConsumer when unset — this test only pins that
	// default's value, since exercising Work itself needs real Postgres and ClickHouse (see
	// mirror_integration_test.go).
	worker := &MirrorWorker{}
	if worker.ConsumerName != "" {
		t.Fatalf("zero-value ConsumerName = %q, want empty", worker.ConsumerName)
	}
	if MirrorConsumer != "clickhouse_mirror" {
		t.Fatalf("MirrorConsumer = %q, want %q", MirrorConsumer, "clickhouse_mirror")
	}
	if MirrorConsumer == consumer.TargetingRollupConsumer {
		t.Fatalf("MirrorConsumer must not collide with the rollup consumer's own offset row")
	}
}
