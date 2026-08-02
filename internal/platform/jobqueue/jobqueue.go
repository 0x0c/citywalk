// Package jobqueue wires CW-0010 Unit 8's PostgreSQL-backed job queue: a river client built against
// this codebase's existing pgx/v5 pool (internal/platform/postgres), so a job can be enqueued from
// inside the same transaction that saves the definition triggering it — the property a queue in a
// separate system cannot offer, and the reason CW-0010 settled on river over an external broker.
//
// It also holds the 15-minute time-zone-slot scheduling primitive Unit 8 names (tzslot.go) and a
// periodic job that exercises it end to end (activationslot.go). See activationslot.go's package doc
// comment for why that job is a placeholder rather than real campaign activation.
package jobqueue

import (
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Client is the concrete river client type this codebase uses. Every migration and enqueue this
// package performs goes through pgx/v5 transactions (CW-0010 Unit 2's own pgx choice), matching the
// pool internal/platform/postgres already builds.
type Client = river.Client[pgx.Tx]

// maxWorkers bounds how many jobs this process works concurrently on the default queue. Phase one
// (CW-0010 Unit 11) runs every job kind on one queue in the same process as the rest of the server;
// the three jobs this pass registers are all periodic, low-frequency, and individually cheap (a
// membership recompute, a rollup drain), so a small fixed pool is enough headroom without adding a
// second queue to reason about.
const maxWorkers = 10

// New builds the job queue client against pool, with workers and periodicJobs registered up front.
// The caller owns the client's lifecycle (Start/Stop), the same way cmd/server/main.go owns the pool
// and Redis client's lifecycles rather than this package managing them itself.
func New(pool *pgxpool.Pool, workers *river.Workers, periodicJobs []*river.PeriodicJob, logger *slog.Logger) (*Client, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: maxWorkers},
		},
		Workers:      workers,
		PeriodicJobs: periodicJobs,
		Logger:       logger,
	})
	if err != nil {
		return nil, fmt.Errorf("jobqueue: new client: %w", err)
	}
	return client, nil
}
