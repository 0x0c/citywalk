package batch

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/audience/registry"
)

// ReconcileInterval is how often CW-0005 Unit 6's scheduled reconciliation runs. A full recomputation
// reads every segment's membership from Postgres and rewrites both the forward and reverse indexes,
// so it is not free to run constantly; Unit 6 itself frames the disagreement count as a health metric
// ("nonzero and growing" is the signal to act on), not a tight correctness SLA — incremental
// maintenance (Unit 5) is what holds the index current between recomputations. Hourly is frequent
// enough to catch a dependency-map gap or a dropped event within the hour, and infrequent enough that
// the cost scales with segment count, not with request volume.
const ReconcileInterval = time.Hour

// ReconcileArgs carries no data: the job always reconciles every segment's membership against the
// live index, so there is nothing to parameterize per run.
type ReconcileArgs struct{}

// Kind implements river.JobArgs.
func (ReconcileArgs) Kind() string { return "membership_reconcile" }

// ReconcileWorker runs CW-0005 Unit 6's scheduled reconciliation: a full Recompute, whose Report the
// disagreement gauge in batch.go already records regardless of who calls Recompute — this worker's
// only job is to be the schedule.
type ReconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]
	Pool     *pgxpool.Pool
	Redis    *redis.Client
	Registry *registry.Registry
}

// Work implements river.Worker.
func (w *ReconcileWorker) Work(ctx context.Context, job *river.Job[ReconcileArgs]) error {
	if _, err := Recompute(ctx, w.Pool, w.Redis, w.Registry); err != nil {
		return fmt.Errorf("batch: scheduled reconciliation: %w", err)
	}
	return nil
}

// ReconcilePeriodicJob registers ReconcileWorker to run on ReconcileInterval, using river's periodic
// job scheduler (CW-0010 Unit 8) rather than a hand-rolled ticker — the queue and the schedule that
// feeds it live in the same system, which is the point of choosing a Postgres-backed queue at all.
func ReconcilePeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(ReconcileInterval),
		func() (river.JobArgs, *river.InsertOpts) { return ReconcileArgs{}, nil },
		&river.PeriodicJobOpts{ID: "membership_reconcile"},
	)
}
