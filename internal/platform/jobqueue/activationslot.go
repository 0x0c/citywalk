package jobqueue

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
)

// ActivationSlotArgs names which of the 96 15-minute slots (tzslot.go) just elapsed.
type ActivationSlotArgs struct {
	Slot int `json:"slot"`
}

// Kind implements river.JobArgs.
func (ActivationSlotArgs) Kind() string { return "channel_activation_slot" }

// ActivationSlotWorker is CW-0010 Unit 8's proof that the 15-minute slot math drives a real river
// periodic job, not only unit tests. It is a placeholder, not real campaign activation: Unit 8's
// design buckets channels into slots by time-zone offset, but no channel carries a stored time-zone
// offset yet — internal/channel/register.Register writes "locale, time zone, application version"
// into the channels.attributes jsonb document as whatever keys the caller's JSON happens to use
// (internal/platform/connectserver's ChannelServer.Register decodes attributes_json opaquely), with
// no registered attribute name, no column, and nothing selectable by offset in SQL. Wiring real
// per-slot activation therefore needs a channel time-zone field first — CW-0004's attribute registry
// would need to name it, and Postgres would need a way to query channels by it — which is a
// prerequisite this pass does not build, per its own scope.
//
// What this job does instead: every SlotDuration, it computes which slot the firing instant falls
// into (SlotForTimeOfDay) and logs it. That exercises the real river periodic-job scheduler, the real
// slot math, and the real 15-minute cadence end to end; the only thing missing is a channel population
// to act on.
type ActivationSlotWorker struct {
	river.WorkerDefaults[ActivationSlotArgs]
	Logger *slog.Logger
}

// Work implements river.Worker.
func (w *ActivationSlotWorker) Work(ctx context.Context, job *river.Job[ActivationSlotArgs]) error {
	w.Logger.InfoContext(ctx, "channel activation slot elapsed; no channel time zone field exists yet to act on it",
		slog.Int("slot", job.Args.Slot))
	return nil
}

// ActivationSlotPeriodicJob registers ActivationSlotWorker to fire once per SlotDuration, computing
// the current slot from the firing time itself (river's periodic-job scheduler, per CW-0010 Unit 8 —
// not a hand-rolled ticker).
func ActivationSlotPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(SlotDuration),
		func() (river.JobArgs, *river.InsertOpts) { return newActivationSlotArgs(time.Now()), nil },
		&river.PeriodicJobOpts{ID: "channel_activation_slot"},
	)
}

// newActivationSlotArgs is ActivationSlotPeriodicJob's constructor logic, pulled out as a named
// function so it is directly testable: river.PeriodicJob keeps its constructor unexported, so a test
// cannot otherwise observe what a firing would enqueue without a live client.
func newActivationSlotArgs(firedAt time.Time) ActivationSlotArgs {
	return ActivationSlotArgs{Slot: SlotForTimeOfDay(firedAt)}
}
