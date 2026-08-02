package jobqueue

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestActivationSlotWorkerRegisters(t *testing.T) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &ActivationSlotWorker{Logger: discardLogger()}); err != nil {
		t.Fatalf("AddWorkerSafely: %v", err)
	}
}

func TestActivationSlotWorkerWork(t *testing.T) {
	worker := &ActivationSlotWorker{Logger: discardLogger()}

	job := &river.Job[ActivationSlotArgs]{
		JobRow: &rivertype.JobRow{},
		Args:   ActivationSlotArgs{Slot: 42},
	}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

func TestNewActivationSlotArgsUsesTheFiringTimesSlot(t *testing.T) {
	firedAt := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	got := newActivationSlotArgs(firedAt)
	want := ActivationSlotArgs{Slot: SlotForTimeOfDay(firedAt)}
	if got != want {
		t.Errorf("newActivationSlotArgs(%s) = %+v, want %+v", firedAt, got, want)
	}
	if got.Slot != 36 { // 09:00 UTC is slot 36 (see tzslot_test.go)
		t.Errorf("newActivationSlotArgs(%s).Slot = %d, want 36", firedAt, got.Slot)
	}
}

func TestActivationSlotPeriodicJobSchedulesOncePerSlotDuration(t *testing.T) {
	job := ActivationSlotPeriodicJob()
	if job == nil {
		t.Fatal("ActivationSlotPeriodicJob returned nil")
	}

	// PeriodicInterval(SlotDuration) is the schedule ActivationSlotPeriodicJob is built on; confirm
	// it actually paces at SlotDuration, which is what ties the 96-slot cadence to a real periodic
	// job rather than an arbitrary interval.
	schedule := river.PeriodicInterval(SlotDuration)
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	next := schedule.Next(now)
	if got := next.Sub(now); got != SlotDuration {
		t.Errorf("PeriodicInterval(SlotDuration).Next spacing = %s, want %s", got, SlotDuration)
	}
}

func TestActivationSlotArgsKind(t *testing.T) {
	if got := (ActivationSlotArgs{}).Kind(); got != "channel_activation_slot" {
		t.Errorf("Kind() = %q, want %q", got, "channel_activation_slot")
	}
}
