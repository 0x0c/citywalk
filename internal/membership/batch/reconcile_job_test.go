package batch

import (
	"testing"
	"time"

	"github.com/riverqueue/river"
)

func TestReconcileArgsKind(t *testing.T) {
	if got := (ReconcileArgs{}).Kind(); got != "membership_reconcile" {
		t.Errorf("Kind() = %q, want %q", got, "membership_reconcile")
	}
}

func TestReconcileWorkerRegisters(t *testing.T) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &ReconcileWorker{}); err != nil {
		t.Fatalf("AddWorkerSafely: %v", err)
	}
}

func TestReconcilePeriodicJobSchedulesOnReconcileInterval(t *testing.T) {
	if ReconcilePeriodicJob() == nil {
		t.Fatal("ReconcilePeriodicJob returned nil")
	}

	schedule := river.PeriodicInterval(ReconcileInterval)
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if got := schedule.Next(now).Sub(now); got != ReconcileInterval {
		t.Errorf("PeriodicInterval(ReconcileInterval).Next spacing = %s, want %s", got, ReconcileInterval)
	}
	if ReconcileInterval != time.Hour {
		t.Errorf("ReconcileInterval = %s, want 1h (a health-metric cadence, not a tight SLA)", ReconcileInterval)
	}
}
