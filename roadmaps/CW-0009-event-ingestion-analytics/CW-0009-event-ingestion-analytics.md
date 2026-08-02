**English** · [日本語](CW-0009-event-ingestion-analytics-ja.md)

# CW-0009 — Event ingestion and measurement pipeline

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0009](CW-0009-event-ingestion-analytics.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **In progress** |
| Topic | Measurement |
| Related | [CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md), [CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md), [CW-0008](../CW-0008-deterministic-experiment-assignment/CW-0008-deterministic-experiment-assignment.md) |
<!-- /CW-METADATA -->

## Introduction

We propose an ingestion path that accepts an event batch, validates it cheaply, appends it to a
durable log, and returns — with every consumer reading from that log rather than from the request.
The log fans out to three readers with unrelated deadlines: the targeting rollups that
[CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md) reads within
minutes, a columnar store that answers campaign reports within seconds of a query, and an export for
an external data platform. Deduplication is by a client-generated, time-ordered event identifier
resolved at merge time in the columnar store, so a device may resend freely.

The log in the middle is the design. Ingestion and reporting have nothing in common except the data,
and putting a durable seam between them is what lets a report be rebuilt, an aggregation be changed,
or a consumer be added without touching the endpoint devices depend on.

## Motivation

Events serve two purposes that are easy to conflate and must not share a code path. Measurement uses
them to tell a campaign owner what happened, and targeting uses them to decide who qualifies for a
campaign — the "opened this screen three times in the last week" condition from
[CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md) reads the same
stream a report does. The two readers want incompatible shapes: targeting wants a small current
counter per channel and event name, and reporting wants every event retained, sliced by campaign,
variant, and time.

Writing directly to a store on the request path is what fails here, and it fails on coupling before
it fails on throughput. citywalk's projected volume is roughly 50 million events a day — around 600
per second on average and a few thousand at peak — which a single relational instance can absorb.
What it cannot absorb is that the same instance then serves the report queries, so a campaign owner
opening a dashboard slows event acceptance for every device, and a schema change for a new report
requires a migration on the table devices are writing into. The volume is survivable; the coupling is
not.

The second thing the request path cannot do is redo. A report definition will change — a conversion
window widens, a metric turns out to be counting dismissals wrong — and recomputing it requires the
raw events still being there in a form a new aggregation can read. An aggregate written in place at
ingestion time cannot be recomputed, so the first wrong definition is permanent.

## Detailed design

### Unit 1 — The event envelope

Every event carries a client-generated identifier, the channel identifier, the event name, the device
time at which it occurred, and a property map. Impression-family events additionally carry the
message identifier, the variant identifier recorded at display, and — for a suppression — the reason
from the closed set defined in
[CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md).

The identifier is a time-ordered universally unique identifier, generated on the device. Time
ordering is what makes it cheap to index and to range-scan in the columnar store, where a random
identifier would scatter writes across the whole key space.

Each event carries two timestamps: the device time it occurred, and the server time it was received.
Keeping both is necessary because neither alone is usable. Device time is what the analysis means but
is manipulable and can be wrong; server time is trustworthy but is hours late for an event that
accumulated offline. The pipeline stores both, reports on device time corrected by the measured
clock offset and clamped to the receipt time, and recomputes the last several days of aggregates on a
schedule so that late arrivals land in the day they belong to.

### Unit 2 — Acceptance

The endpoint validates the batch's shape, rejects events naming an unknown event type, applies a
per-channel rate limit, appends the batch to the log, and acknowledges. It performs no aggregation,
no deduplication, and no lookup against campaign state, which is what keeps its latency independent
of everything downstream.

The rate limit protects the platform from one device, not from a user: a bug in a release can put an
event in a render loop, and without a per-channel ceiling that single device saturates the ingestion
path for everyone. Rejected volume is counted and reported by channel, so the bug is visible rather
than merely absorbed.

### Unit 3 — The log as the seam

The log is partitioned by channel identifier, which keeps one channel's events in order and lets
consumers scale by partition. Each consumer tracks its own position, so a slow or failed consumer
falls behind without affecting the others, and a consumer can be replayed from a retained offset to
rebuild its output.

Delivery is at least once, and every consumer is therefore required to be idempotent — a requirement
stated here rather than left to each consumer, because a consumer that quietly is not idempotent
produces double counts that look like real traffic.

### Unit 4 — Storage and deduplication

Events land in a columnar store, partitioned by day and ordered within a partition by project,
message, and time — the order that campaign reports scan. Deduplication happens at merge time: rows
sharing an event identifier collapse to one, so a device resending a batch after a timeout costs
storage briefly and nothing thereafter.

Resolving duplicates at merge rather than at write is what keeps acceptance cheap. Checking for a
prior occurrence at write time would put a read before every write on the highest-volume path in the
platform, to prevent a duplicate that the storage engine removes for free during compaction anyway.

### Unit 5 — Aggregation

Two rollups are maintained incrementally rather than computed per query. The **targeting rollup** is
a count per channel, event name, and day, which is what
[CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md)'s windowed
conditions sum. The **campaign rollup** is a count per message, variant, hour, and event type, which
is what a report reads.

Unique reach is estimated with a probabilistic distinct-count sketch rather than counted exactly,
because an exact distinct count over an arbitrary slice requires either retaining the identifiers per
slice or scanning the raw events. The sketches merge, so a daily figure combines into a weekly one
without rescanning, and the reported error is around one percent — well inside what any decision made
from a reach number is sensitive to. Impression and click counts stay exact, since those are sums and
cost nothing to keep exact.

### Unit 6 — Conversion attribution

A campaign names a conversion event and a window. A conversion is counted when a channel that
received an impression emits the named event within the window, attributed to the variant recorded on
that impression.

The attribution rule is stated explicitly because every alternative reading is defensible and they
disagree: attribution is to the most recent impression of that campaign preceding the conversion,
one conversion counts once per campaign per window, and a channel in the holdout that emits the event
counts toward the holdout's rate. Writing the rule down is what makes two reports of the same
campaign comparable.

### Unit 7 — The suppression report

Suppression events aggregate into a per-campaign breakdown by reason, alongside the impression
counts. A campaign owner therefore reads how many devices were eligible, how many were shown, and how
many were suppressed for each reason.

This report is the one that answers the question campaign owners actually ask when a campaign
underperforms, and it is only answerable because
[CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md) emits suppressions from a
closed set of reasons.

## Alternatives considered

- **Write events to the relational store on the request path.** Rejected on coupling. Report queries
  and device writes would share an instance, so a dashboard slows ingestion, and every reporting
  schema change becomes a migration on the table devices write into.
- **Aggregate synchronously at ingestion and keep no raw events.** Rejected. It makes the first
  aggregation definition permanent, so a widened conversion window or a corrected metric cannot be
  applied to history — and a metric definition being wrong once is close to certain.
- **A managed cloud data warehouse instead of a self-run columnar store.** Rejected for the serving
  path, and worth revisiting for the export. Query latency in the tens of seconds is fine for
  analysis and wrong for the interactive report a campaign owner refreshes, and per-query pricing
  makes an embedded dashboard's cost scale with how often it is looked at.
- **Exactly-once ingestion through a transactional protocol.** Rejected as unnecessary. At-least-once
  delivery plus a merge-time collapse on the event identifier produces the same observable result,
  without a distributed transaction on the platform's highest-volume path.
- **Skipping the log and having each consumer read the columnar store.** Rejected. The targeting
  rollup needs minute-level freshness, which an analytical store's batch ingestion does not provide,
  and it would make targeting depend on the availability of a system whose outage should only delay
  reports.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [ ] Unit 1 — The event envelope, time-ordered identifiers, and the two-timestamp rule.
      The envelope (`internal/event/model`), the closed set of kinds, the impression-family field
      requirements, and storing both device time and server time are implemented and tested. Also
      built: a clock-offset sanity bound, `model.ClockSkewImplausible`, that flags rather than
      silently trusts a device time diverging from receipt time past tolerance, surfaced per batch as
      `ingest.Result.ClockSkewFlagged` and as an OpenTelemetry counter. Its `MaxFutureSkew` and
      `MaxPastSkew` tolerances are documented judgement calls, since neither `docs/requirements.md`
      nor this design names a concrete threshold. Also built: `model.EffectiveTime`, which every
      rollup writer in `internal/event/consumer` now buckets by, including `suppression_rollup` — the
      one writer that was still bucketing by raw `device_time`, fixed here to the same receipt-time
      clamp the targeting and campaign rollups already used.

      Also now built: the scheduled recomputation this unit calls for.
      `internal/event/consumer/recompute_job.go` registers `RollupRecomputeWorker`, a `river.Worker`
      (CW-0010 Unit 8), as a periodic job that drains `events_log` into the rollups every minute,
      matching Unit 5's own "minute-level freshness" framing. The job does not re-scan a fixed
      trailing window of days. The consumer already advances a monotonic per-consumer offset and
      applies each event exactly once, through an additive `ON CONFLICT ... DO UPDATE`; re-scanning
      a window on top of that would re-apply already-counted events and double-count them. Draining
      the offset-tracked backlog on a schedule delivers the same outcome for an arrival of any
      lateness, not only a chosen window of days: a late arrival lands in the day it belongs to.
      `recompute_job.go`'s own comment records this reasoning in full.

      Still not built: estimating a device's typical clock offset and correcting for it — the "device
      time corrected by the measured clock offset" half of this unit's design text, distinct from the
      clamp above, which only bounds an implausible timestamp rather than adjusting a plausible one
      for a device's steady skew. `model.EffectiveTime`'s doc comment states this explicitly as out of
      scope for the change that added the clamp, and it remains open.
- [x] Unit 2 — Cheap acceptance with per-channel rate limiting and rejection metrics.
      `internal/event/ingest` validates each event independently (one bad event does not sink the
      rest of its batch), enforces a per-channel fixed-window rate limit in Redis
      (`internal/event/ratelimit`), and records rejected volume by channel and reason as an
      OpenTelemetry counter.
- [x] Unit 3 — The durable log, partitioned by channel, with per-consumer positions.
      Phase one, `internal/event/consumer.RunOnce` against the Postgres table
      `migrations/0006_events.sql`, stays the active path, keeping per-consumer offsets, replay from a
      stored position, and at-least-once-safe consumption. A second, real path exists behind CW-0010
      Unit 5's log: `internal/platform/eventlog`, `internal/event/ingest.LogPublisher`, and
      `internal/event/consumer.RunOnceFromLog`. `internal/platform/config`'s CITYWALK_EVENT_PUBLISHER
      flag selects it and defaults to Postgres, so this stays an alternate path, not a cutover.
      Partitioning by channel identifier is real (`eventlog.PartitionKey`, tested for determinism and,
      against a real broker, for the per-channel ordering partitioning exists to guarantee), and
      per-consumer position tracking is real too: `event_consumer_offsets` for the Postgres path, a
      Kafka consumer group's committed offsets for the log path.

      This pass closes the one design rule that stayed unmet: every consumer must be idempotent under
      at-least-once delivery. `RunOnce`'s offset advance and its rollup writes already shared one
      Postgres transaction, but `RunOnceFromLog`'s position (the log's committed offset) and its rollup
      writes sit in two systems that can fail independently between their two commits, so a crash
      between them reprocessed a batch and double-counted it, since `applyBatch`'s writes are
      `count = count + 1` rather than deduplicated by event identifier. The fix sits at the rollup
      writes themselves, not at either consumer's position tracking: `applyBatch`
      (`internal/event/consumer`) now records every event it processes in `rollup_applied_events`
      (`migrations/0014_rollup_applied_events.sql`) — an `INSERT ... ON CONFLICT (event_id) DO NOTHING`
      guard keyed by event id, in the same transaction as the rollup writes that event drives — and
      skips an event whose id is already recorded rather than re-incrementing `targeting_rollup`,
      `campaign_rollup`, `reach_sketch`, or `suppression_rollup`. One record per event is enough
      regardless of which of those tables it touches, because an event's kind and message id, both
      fixed at creation, fully determine that fixed set of writes, so a replay resolves to exactly the
      writes the first pass already made. `RunOnce` and `RunOnceFromLog` both get this through the
      shared `applyBatch`, unchanged in either consumer's own position-tracking logic, matching the
      design's rule that the two differ only in where the next event comes from. The table is
      deliberately not pruned, unlike a bounded change log — pruning would reopen the exact
      double-count this table exists to prevent for any event older than a retention window — and the
      migration's own comment records why that is safe here: its per-row footprint tracks `events_log`,
      a table this codebase already keeps unpruned in phase one, so it adds no new order of growth.

      Proven directly against real Postgres:
      `internal/event/consumer.TestApplyBatchIsIdempotentAcrossSeparateTransactions`
      (`//go:build integration`) applies the same batch through `applyBatch` twice, in two separate
      transactions — the shape of the actual failure, since `RunOnce`'s own transaction never lets it
      observe a retry — and asserts every rollup lands each event's count exactly once, not twice. This
      sandbox still has no reachable Kafka-compatible broker (nothing listens on port 9092 here), so
      `RunOnceFromLog`'s produce-consume-commit round trip against a real broker remains untested, as
      before; what changed is that the mechanism making any replay safe, from either consumer, is now
      proven against real Postgres rather than left as a documented gap.
- [x] Unit 4 — Columnar storage with merge-time deduplication by event identifier.
      Phase one keeps its write-time approximation. Deduplication by event identifier still happens
      through events_log's primary key and `ON CONFLICT DO NOTHING` at insert
      (`internal/event/ingest.appendToLog`). That stays an engine-checked write-time constraint, not a
      merge-time collapse. events_log still plays both the log's role and the store's role on the same
      Postgres table.

      What changed is that this unit's actual design now exists as real, tested code: a separate
      columnar store, day-partitioned, deduplicated at merge time. CW-0010 Unit 11 authorizes exactly
      this — "the codebase builds and tests each [second-phase component] against the second phase's
      design" — and Unit 6's own progress note records the same build. `internal/platform/clickhouse`'s
      `events_raw` table is day-partitioned (`PARTITION BY toDate(device_time)`) and ordered by
      `(message_id, device_time, id)`: this unit's "project, message, and time," with message standing
      in for project, since this codebase names no such concept yet. A `ReplacingMergeTree(server_time)`
      engine, keyed on that same ordering, deduplicates at merge time — the mechanism this unit calls
      for, the one Postgres's `ON CONFLICT DO NOTHING` approximated rather than implemented.
      `internal/event/mirror`'s periodic job lands events there, reading `events_log` independently of
      the rollup consumer.

      This is CW-0010 Unit 11's second phase, not the default: `config.ClickHouseMirrorEnabled`
      defaults to false, so a phase-one deployment never writes to `events_raw` at all, and
      events_log's write-time constraint remains the only deduplication actually running until an
      operator opts in. It is also unverified against a live server — no ClickHouse instance was
      reachable in the sandbox this pass was built in (port 8123 closed), so `events_raw`'s
      merge-time collapse is proven by a `//go:build integration` suite
      (`internal/platform/clickhouse/clickhouse_integration_test.go`) that is written and compiles but
      has not actually been run. This box is checked because the unit's design — the store, its
      schema, and a real writer into it — is genuinely built and tested to the extent a sandbox
      without ClickHouse allows, not because it has replaced the write-time approximation as this
      pipeline's active behavior.
- [x] Unit 5 — Targeting and campaign rollups, with sketch-based unique reach.
      `targeting_rollup` and `campaign_rollup` (exact counts) and `reach_sketch` (a real HyperLogLog,
      `internal/event/hll`, merged across days without rescanning raw events) are implemented and
      tested end to end against real Postgres, including the sketch's accuracy and its mergeability.
- [x] Unit 6 — Conversion attribution with an explicitly stated rule.
      `internal/event/attribution` attributes to the most recent qualifying exposure preceding the
      conversion within the window, counts a holdout qualification the same way as a variant
      impression (so holdout and variant rates are comparable), and is idempotent under replay —
      all tested against real Postgres.
- [x] Unit 7 — The per-campaign suppression breakdown by reason.
      `internal/event/report.Suppressions` reads the breakdown from `suppression_rollup` alongside
      the impression count already in `campaign_rollup`. CW-0007 does not exist yet to emit real
      suppression events, so this pass anticipates its closed set of reasons directly in
      `internal/event/model` (documented there) — CW-0007, when built, must emit exactly those
      values rather than defining its own.

## References

- [CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md) — the
  predicate engine whose windowed conditions read the targeting rollup.
- [CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md) — the governance layer that
  emits suppression events from a closed set of reasons.
- [CW-0008](../CW-0008-deterministic-experiment-assignment/CW-0008-deterministic-experiment-assignment.md)
  — the assignment scheme whose recorded variant this pipeline attributes impressions to.
- [`docs/requirements.md`](../../docs/requirements.md) — the measurement and reporting requirements
  this pipeline satisfies.
