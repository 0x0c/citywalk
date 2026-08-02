package consumer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/governance/budget"
)

// RollupRecomputeInterval is how often CW-0009 Unit 1's scheduled recomputation runs: RunOnce drains
// events_log into the rollups, and Unit 5's own motivation states the targeting rollup "needs
// minute-level freshness" — this is that cadence, not a separately chosen number.
const RollupRecomputeInterval = time.Minute

// rollupRecomputeBatchLimit bounds how many events one RunOnce call inside a single firing processes,
// matching RunOnce's own batchLimit parameter. 1,000 keeps one transaction (RunOnce commits its
// whole batch atomically) short even under a burst, while RollupRecomputeWorker.Work's drain loop
// (below) still clears an arbitrarily large backlog within one firing by calling RunOnce repeatedly.
const rollupRecomputeBatchLimit = 1000

// RollupRecomputeArgs carries no data: the job always drains whatever events_log has accumulated
// since the consumer's last recorded offset, so there is nothing to parameterize per run.
type RollupRecomputeArgs struct{}

// Kind implements river.JobArgs.
func (RollupRecomputeArgs) Kind() string { return "event_rollup_recompute" }

// RollupRecomputeWorker is CW-0009 Unit 1's "recomputes the last several days of aggregates on a
// schedule so that late arrivals land in the day they belong to."
//
// What "recompute" means here deliberately is not re-scanning a fixed trailing window of raw events:
// this consumer already advances a monotonic per-consumer offset (Unit 3) and applies every event
// exactly once via an additive ON CONFLICT ... DO UPDATE SET count = count + 1 (consumer.go). Adding
// a bounded-window re-scan on top of that would re-apply events already counted and double-count
// them — the offset exists precisely so a retry or a rerun reprocesses forward, never backward, over
// the same events. A late-arriving event is not one whose device_time is old; every event's rollup
// bucket is already the corrected, receipt-time-clamped bucketTime (effectiveTime in consumer.go) as
// soon as it is consumed. What can be "late" under this design is the event's arrival at events_log
// itself — offline queuing, a slow upload, a retried batch — and RunOnce already picks up any event
// currently in events_log the moment it next runs, however long ago its device_time claims to be.
//
// So the schedule this job adds is the missing piece: RunOnce (this package) has never been called
// from anywhere outside a test before this pass. Without a periodic caller, nothing drains
// events_log at all, and every event — on time or late — sits unrolled-up indefinitely. Running it on
// a schedule, draining the full backlog each time, is what actually makes "late arrivals land in the
// day they belong to" true, for arrivals of any lateness, not only a chosen window of days.
type RollupRecomputeWorker struct {
	river.WorkerDefaults[RollupRecomputeArgs]
	Pool          *pgxpool.Pool
	ConsumerName  string
	BudgetCounter *budget.Counter
}

// Work implements river.Worker. It calls RunOnce repeatedly until a call processes fewer than
// rollupRecomputeBatchLimit events (RunOnce's own signal that nothing more is currently pending),
// which is what lets one firing clear a backlog larger than one batch rather than only ever making
// partial progress against a burst.
func (w *RollupRecomputeWorker) Work(ctx context.Context, job *river.Job[RollupRecomputeArgs]) error {
	consumerName := w.ConsumerName
	if consumerName == "" {
		consumerName = TargetingRollupConsumer
	}
	for {
		processed, err := RunOnce(ctx, w.Pool, consumerName, rollupRecomputeBatchLimit, w.BudgetCounter)
		if err != nil {
			return fmt.Errorf("consumer: scheduled rollup recompute: %w", err)
		}
		if processed < rollupRecomputeBatchLimit {
			return nil
		}
	}
}

// RollupRecomputePeriodicJob registers RollupRecomputeWorker to run on RollupRecomputeInterval, using
// river's periodic job scheduler (CW-0010 Unit 8) rather than a hand-rolled ticker. RunOnStart is set
// so a freshly started process does not wait a full interval before its first drain.
func RollupRecomputePeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(RollupRecomputeInterval),
		func() (river.JobArgs, *river.InsertOpts) { return RollupRecomputeArgs{}, nil },
		&river.PeriodicJobOpts{ID: "event_rollup_recompute", RunOnStart: true},
	)
}
