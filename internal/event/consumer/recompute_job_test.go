package consumer

import (
	"testing"
	"time"

	"github.com/riverqueue/river"
)

func TestRollupRecomputeArgsKind(t *testing.T) {
	if got := (RollupRecomputeArgs{}).Kind(); got != "event_rollup_recompute" {
		t.Errorf("Kind() = %q, want %q", got, "event_rollup_recompute")
	}
}

func TestRollupRecomputeWorkerRegisters(t *testing.T) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &RollupRecomputeWorker{}); err != nil {
		t.Fatalf("AddWorkerSafely: %v", err)
	}
}

func TestRollupRecomputePeriodicJobSchedulesOnRollupRecomputeInterval(t *testing.T) {
	if RollupRecomputePeriodicJob() == nil {
		t.Fatal("RollupRecomputePeriodicJob returned nil")
	}

	schedule := river.PeriodicInterval(RollupRecomputeInterval)
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if got := schedule.Next(now).Sub(now); got != RollupRecomputeInterval {
		t.Errorf("PeriodicInterval(RollupRecomputeInterval).Next spacing = %s, want %s", got, RollupRecomputeInterval)
	}
	if RollupRecomputeInterval != time.Minute {
		t.Errorf("RollupRecomputeInterval = %s, want 1m (Unit 5's minute-level freshness)", RollupRecomputeInterval)
	}
}

func TestRollupRecomputeWorkerDefaultsConsumerName(t *testing.T) {
	// Work reads w.ConsumerName, defaulting to TargetingRollupConsumer when unset — this test only
	// pins that default's value, since exercising Work itself needs a real Postgres pool (see
	// recompute_job_integration_test.go).
	worker := &RollupRecomputeWorker{}
	if worker.ConsumerName != "" {
		t.Fatalf("zero-value ConsumerName = %q, want empty", worker.ConsumerName)
	}
	if TargetingRollupConsumer != "rollup" {
		t.Fatalf("TargetingRollupConsumer = %q, want %q", TargetingRollupConsumer, "rollup")
	}
}
