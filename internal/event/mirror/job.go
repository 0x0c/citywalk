package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/0x0c/citywalk/internal/platform/clickhouse"
)

// MirrorInterval is how often the mirroring job drains events_log into ClickHouse. Unlike
// consumer.RollupRecomputeInterval's minute-level cadence — driven by the targeting rollup's
// freshness need (CW-0009 Unit 5) — nothing on any request path reads events_raw; five minutes keeps
// the ClickHouse write cheap and batched without leaving the interactive report (CW-0010 Unit 6) so
// far behind that a refresh looks stale.
const MirrorInterval = 5 * time.Minute

// MirrorArgs carries no data: like RollupRecomputeArgs, the job always drains whatever events_log has
// accumulated since MirrorConsumer's last recorded offset.
type MirrorArgs struct{}

// Kind implements river.JobArgs.
func (MirrorArgs) Kind() string { return "event_clickhouse_mirror" }

// MirrorWorker is CW-0010 Unit 11's config-gated second-phase measurement path. cmd/server/main.go
// registers it only when config.Config.ClickHouseMirrorEnabled is true and a ClickHouse DSN is set;
// otherwise this worker is never constructed and no ClickHouse connection is ever opened.
type MirrorWorker struct {
	river.WorkerDefaults[MirrorArgs]
	Pool         *pgxpool.Pool
	Client       *clickhouse.Client
	ConsumerName string
}

// Work implements river.Worker, draining the full backlog in one firing the same way
// consumer.RollupRecomputeWorker.Work does: call RunOnce repeatedly until a call processes fewer than
// batchLimit events, RunOnce's own signal that nothing more is currently pending.
func (w *MirrorWorker) Work(ctx context.Context, job *river.Job[MirrorArgs]) error {
	consumerName := w.ConsumerName
	if consumerName == "" {
		consumerName = MirrorConsumer
	}
	for {
		processed, err := RunOnce(ctx, w.Pool, w.Client, consumerName)
		if err != nil {
			return fmt.Errorf("mirror: scheduled clickhouse mirror: %w", err)
		}
		if processed < batchLimit {
			return nil
		}
	}
}

// MirrorPeriodicJob registers MirrorWorker to run on MirrorInterval, using river's periodic job
// scheduler (CW-0010 Unit 8) the same way consumer.RollupRecomputePeriodicJob and
// batch.ReconcilePeriodicJob already do, rather than a hand-rolled ticker.
func MirrorPeriodicJob() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(MirrorInterval),
		func() (river.JobArgs, *river.InsertOpts) { return MirrorArgs{}, nil },
		&river.PeriodicJobOpts{ID: "event_clickhouse_mirror", RunOnStart: true},
	)
}
