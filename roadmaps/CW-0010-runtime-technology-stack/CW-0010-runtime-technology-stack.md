**English** · [日本語](CW-0010-runtime-technology-stack-ja.md)

# CW-0010 — Runtime and technology stack

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0010](CW-0010-runtime-technology-stack.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **In progress** |
| Topic | Platform |
| Related | [CW-0001](../CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md), [CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md), [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md), [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) |
<!-- /CW-METADATA -->

## Introduction

We propose the runtime and the stores the in-app message platform runs on, chosen against a load
estimate stated up front rather than against a scale the platform might one day reach. The services
are Go; the durable record is PostgreSQL; the hot lookups and counters are Redis; the event seam is a
Kafka-compatible log; measurement lives in ClickHouse; creative assets sit in object storage behind a
content delivery network. Adoption is staged, and this item states which components the first phase
does without.

The load estimate is the load-bearing part of the argument. At citywalk's target scale the delivery
path is a few hundred requests per second, which is small — and the temptation this creates is to
build for a scale the numbers do not support, adding operational surface that costs a small team more
than the throughput is worth.

## Motivation

A technology selection made without a load estimate is a selection made from habit, and the two
failure directions are equally expensive. Under-building shows up late and loudly: a store chosen for
convenience stops answering under a growth curve nobody projected, and the migration happens under
pressure. Over-building shows up early and quietly: five stateful systems arrive before the first
campaign ships, and every incident afterward costs an on-call engineer a system they have never
debugged.

The numbers below decide between the two, so they come before any component. They also make the
choices auditable — a reader who disagrees can point at an assumption rather than at a preference.

The one place the estimate does not decide is the event pipeline, and that exception is worth naming
because it looks like over-building. At 600 events per second, a durable log is not a throughput
requirement; it is a decoupling requirement, for the reason
[CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) gives about
three consumers with unrelated deadlines. Adopting it is a decision about structure, and the estimate
is silent on structure.

## Detailed design

### Unit 1 — The load estimate

The figures below assume the upper bound of the target range: 10 million monthly active users, a
quarter of them active on a given day, at 2.5 sessions each.

| Quantity | Estimate | Derivation |
|---|---|---|
| Daily active users | 2.5 million | 25 percent of 10 million |
| Sessions per day | 6.25 million | 2.5 per active user |
| Payload synchronizations | 6.25 million per day, about 70 per second average, about 430 at peak | one per session, peak factor 6 |
| Unchanged responses | about 95 percent of synchronizations | campaign edits are a few per day |
| Events | 50 million per day, about 600 per second average, about 3,500 at peak | 8 per session |
| Delivery egress | about 13 gigabytes per day | 5 percent full responses at 40 kilobytes compressed |
| Channel records | 10 million rows | one per installed device |
| Event storage | about 3 gigabytes per day compressed, about 1 terabyte at 13 months | 50 million rows at roughly 400 bytes raw |
| Reverse membership index | about 1 gigabyte | 10 million channels at about 100 bytes |

The synchronization figure is the one that sets the shape of the delivery tier: a few hundred
requests per second, of which nineteen in twenty return no body, is a workload a handful of
application instances serve with headroom. The requirement of 1,000 requests per second in
[`docs/requirements.md`](../../docs/requirements.md) therefore carries roughly a factor of two over
the peak estimate.

### Unit 2 — Language and service framework

The services are Go. The delivery and ingestion paths are concurrent, allocation-sensitive, and
mostly waiting on stores, which is the workload Go's scheduler and its standard library are shaped
for; the operational properties matter as much as the runtime ones, since a single static binary
removes a class of deployment problem, and the startup time makes horizontal scaling responsive
during a traffic spike.

Service interfaces are defined in Protocol Buffers and served over Connect, which speaks both the
gRPC protocol and plain Hypertext Transfer Protocol (HTTP) with JavaScript Object Notation (JSON)
bodies from one definition. The mobile software development kit (SDK) therefore issues ordinary
HTTP requests — no gRPC runtime on the device, and every proxy and debugging tool in the path
works — while internal service-to-service calls use the binary protocol. One schema generating
both sides removes the hand-written client that otherwise drifts from the server.

Libraries settled by this item: `pgx` with `sqlc` for database access, `go-redis`, `franz-go` for the
log, `clickhouse-go`, `cel-go` for predicates, `roaring` for bitmaps, `river` for the job queue, and
the OpenTelemetry Go modules for observability.

### Unit 3 — The durable record

PostgreSQL holds campaign definitions, variants, triggers, policies, segment definitions, channels,
users, attributes, and the audit log. The dataset is tens of millions of rows and the write rate is
low; the requirements that decide the choice are transactional integrity for a definition save,
partial indexes for the delivery service's filtered scans, and a document type for the content
objects that
[CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md) stores whole.

Schema changes are managed as versioned migrations applied in a deployment step, with a rule that a
migration and the code that depends on it never ship together: a column is added and backfilled in
one release, and read in the next. That rule is what makes a rollback possible at every point.

### Unit 4 — Hot lookups and counters

Redis holds what the delivery path reads on every request and what governance updates on every
impression: the reverse membership index from
[CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md), the entity tag
cache and assembly bundles from
[CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md), and the sliding-window
counters from
[CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md).

Everything here is derived or expiring, which is the property that makes the choice safe: losing the
whole store costs a rebuild and a latency spike, not data. The counters are the one exception — a
loss resets a user's project-wide budget — and the design accepts that, because the alternative is
putting a durable write on the impression path to protect a comfort threshold.

Server-side scripting is used for the counter update, so that a read and an increment from two
application instances cannot both observe room for the last impression.

### Unit 5 — The event log

A Kafka-compatible log carries events from ingestion to its three consumers. Redpanda is the default
choice over Kafka itself for a team of this size: one binary, no separate coordination service, and a
materially smaller operational footprint at a volume where Kafka's scaling ceiling is irrelevant.

The estimate does not require a log at 600 events per second, and adopting one anyway is deliberate.
What it buys is the seam: a consumer can be replayed to rebuild an aggregate, a fourth consumer can be
added without touching ingestion, and a slow report cannot slow event acceptance. Building those
properties later means rewriting the ingestion path devices depend on.

### Unit 6 — Measurement storage

ClickHouse stores raw events and the rollups. The workload is append-heavy, queried by aggregation
over a time range and a few dimensions, and needs interactive latency for a report a campaign owner
refreshes; it also needs merge-time deduplication by event identifier, which the engine provides
directly rather than as application logic.

Retention is 13 months for raw events, chosen so a report can compare a campaign against the same
period a year earlier. Rollups are retained indefinitely, being small.

### Unit 7 — Creative assets

Images and Hypertext Markup Language (HTML) content are stored in object storage and served through
a content delivery network, with the payload carrying content-addressed uniform resource locators
(URLs). Content addressing means an asset is immutable and
cacheable forever, and replacing an image produces a new URL rather than a cache invalidation — which
matters because an edited image that reaches half the devices is a campaign displaying two different
things.

The payload itself is not served from the network edge, for the reason
[CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) gives: it varies per channel.

### Unit 8 — Scheduled work

Background work — segment recomputation, rollup maintenance, campaign activation and expiry,
reconciliation — runs on a job queue backed by PostgreSQL, so a job can be enqueued in the same
transaction that saves the definition triggering it. A queue in a separate system cannot offer that,
and the failure it prevents is a definition that saved while its activation job did not.

Time-zone-relative scheduling is handled by bucketing channels into 15-minute offset slots: 96 slots
cover every offset in use, including the ones at 45 minutes past the hour. A campaign scheduled for
09:00 local time enqueues one activation per slot rather than one per channel.

### Unit 9 — Authentication

A device authenticates with a short-lived token, signed by the platform, that binds the channel
identifier; the SDK renews it against a longer-lived registration credential. Binding the channel to
the token is what satisfies the requirement that naming another channel's identifier grants nothing:
the identifier in the request is ignored in favor of the one in the token.

The administrative application programming interface (API) authenticates through the organization's
identity provider and authorizes by role, with viewer, editor, and administrator as the initial set. Every mutation writes an audit
record naming the actor, the target, and the change.

### Unit 10 — Observability

Services emit OpenTelemetry traces, metrics, and structured logs. Beyond the usual service-level
signals, four platform-specific metrics are treated as first-class because each detects a failure
that is otherwise invisible in a healthy-looking system: the payload truncation count
([CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md)), the membership
reconciliation disagreement count
([CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md)), the suppression
count by reason ([CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md)), and the
count of messages skipped for an unsupported schema version
([CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md)).

Every one of those failures produces a campaign with fewer impressions than expected and no error
anywhere, which is why they are named here rather than left to whatever the dashboards happen to
show.

### Unit 11 — Staged adoption

The stack above is the destination, not the first deployment. The first phase runs the definition,
audience, and delivery services as one process against PostgreSQL and Redis, with events written
directly to PostgreSQL and aggregated by scheduled jobs — enough for the first campaigns, and small
enough for one engineer to operate.

The log and ClickHouse arrive in the second phase, when event volume makes the aggregation job's
runtime the constraint. The service split from
[CW-0001](../CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md) is
maintained in the code from the first phase regardless of how many processes run, because separating
services later is cheap when the boundaries already exist and expensive when they do not.

That staging gates when each store carries production traffic. It does not gate when the code may
exist. The first phase's own codebase already holds the Kafka-compatible log, ClickHouse, and the
object storage client. This codebase builds and tests each one against the second phase's design.
A configuration flag selects between them. It defaults to the first phase's PostgreSQL-and-Redis
path.
[CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md)'s ingestion
path and [CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md)'s
asset references get written once, against both phases. Neither needs a rewrite once event volume
forces the second phase open. Flipping the flag is then an operational decision, made once volume
justifies it. Load pressure never forces a second engineering project instead.

## Alternatives considered

- **Kotlin with Spring Boot, or TypeScript with Node.** Both are credible. Go wins on the delivery
  and ingestion paths, which are the platform's hot paths and are concurrency-bound rather than
  logic-bound, and on the operational simplicity of a single static binary. The advantage Kotlin
  would bring is on the administrative side, which is the smallest and least performance-sensitive
  part of the platform.
- **One PostgreSQL instance for everything, including measurement.** Rejected for the destination
  and adopted for the first phase, as Unit 11 sets out. Row-oriented storage scans far more data than
  a columnar engine for the aggregations reports run, and sharing an instance couples report latency
  to event acceptance — the coupling
  [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) rejects.
- **A managed cloud warehouse instead of ClickHouse.** Rejected for the interactive report and worth
  revisiting for the export. Query latency in the tens of seconds does not serve a dashboard a
  campaign owner refreshes, and per-query pricing makes an embedded report's cost scale with how
  often someone looks at it.
- **Kafka rather than Redpanda.** A close call. Kafka's ecosystem is larger and its operational
  knowledge more widely held; Redpanda wins here on footprint, since the platform needs a durable
  ordered log and none of the scaling headroom that justifies Kafka's additional moving parts.
- **A managed function platform instead of long-running services.** Rejected. The delivery path
  depends on in-process caches for the assembly bundles, and a per-request execution model discards
  them, turning a cache hit into a store round trip on the platform's hottest path.
- **gRPC only, with no HTTP and JSON transport.** Rejected on the mobile side. It would require a
  gRPC runtime in the SDK and break the proxies and inspection tools mobile engineers debug with,
  for a payload size difference that compression largely erases.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [ ] Unit 1 — The load estimate, revalidated against measurements once traffic exists.
- [ ] Unit 2 — Go services, Protocol Buffers definitions served over Connect, library baseline.
- [ ] Unit 3 — PostgreSQL schema and the migration-before-code deployment rule.
- [x] Unit 4 — Redis for the membership index, tag cache, assembly bundles, and counters.
      All four named uses are live: the reverse membership index (`internal/membership/reverse`,
      CW-0005 Unit 3), the entity-tag cache (`internal/delivery/etag`, CW-0006 Unit 2), the
      sliding-window budget counters (`internal/governance/budget`, CW-0007 Unit 4), and — the piece
      this box was waiting on — the assembly bundle cache (`internal/delivery/payload/bundle.go`,
      CW-0006 Unit 4), keyed by language, declared schema major, and a hash of the channel's
      membership bitmap, with campaign-keyed invalidation through a Redis set index. Everything here
      is derived or expiring except the counters, exactly as this unit's own text accepts.
- [ ] Unit 5 — The Kafka-compatible log and its consumer positions.
- [ ] Unit 6 — ClickHouse storage, rollups, and the 13-month retention.
- [ ] Unit 7 — Object storage with content-addressed asset URLs behind a delivery network.
- [x] Unit 8 — The PostgreSQL-backed job queue and 15-minute time-zone slots.
      `internal/platform/jobqueue` builds and starts a `river` client against the existing `pgx/v5`
      pool (`internal/platform/postgres`). `cmd/server/main.go` starts and stops that client
      alongside the pool and the Redis client, the way it already owns every other platform
      dependency's lifecycle. `river`'s own schema ships as two migrations,
      `migrations/0011_job_queue.sql` and `migrations/0012_job_queue_pending_state.sql`. They are
      two files, not one, because PostgreSQL refuses to use a `river_job_state` enum value inside the
      same transaction that added it, and this repository's migration runner applies one file per
      transaction; 0011's header explains the split in full. Three periodic jobs — `river`'s own
      scheduler, not a hand-rolled ticker — run on the queue: CW-0005 Unit 6's membership
      reconciliation, CW-0009 Unit 1's rollup recompute, and a proof-of-concept 15-minute
      activation-slot tick. This unit names enqueuing a job inside the same transaction that saves
      the definition triggering it as the reason for a PostgreSQL-backed queue, and that property
      holds for any caller with a `pgx` transaction already open, since `river_job` is an ordinary
      table a transaction can insert into like any other.

      The 15-minute time-zone-slot mechanism (`internal/platform/jobqueue/tzslot.go`) buckets a
      coordinated universal time (UTC) offset into one of 96 slots, including a 45-minute remainder,
      and computes the UTC instant at which a slot's target local time falls. Tests cover every
      offset boundary, the 45-minute offsets, day rollover, and the round trip between a slot and its
      representative offset. A periodic `river` job (`ActivationSlotWorker` and
      `ActivationSlotPeriodicJob`) fires every 15 minutes and logs the slot that elapsed, proving the
      math drives a real job rather than only unit tests.

      What this mechanism does not yet drive is real per-channel activation. No channel carries a
      stored, queryable time-zone offset: `internal/channel/register.Register` writes "time zone"
      into the `channels.attributes` JavaScript Object Notation (JSON) document under whatever key
      the caller's request happens to use, with no registered attribute name, no dedicated column,
      and no way to select channels by offset in structured query language (SQL). `ActivationSlotWorker`
      is a placeholder for that reason, not because the slot math is unproven. Grouping real channels
      by slot and enqueuing their activations needs a channel time-zone field first, which is
      CW-0004's attribute registry's prerequisite to name, not this pass's to add.
- [x] Unit 9 — Channel-bound device tokens, and identity-provider authentication with roles.
      `internal/platform/devicetoken` issues and verifies the device-bound token; ChannelService's
      Register and RefreshToken (`internal/channel/register`) hand it out; DeliveryService and
      EventService are behind an interceptor that rejects a missing or invalid token and binds every
      request to the token's channel identity, never a request field. The administrative half
      (`internal/platform/adminauth`) authorizes by role (viewer, editor, administrator) behind
      AdminService, with every mutation recorded in `message_audit_log` naming the actor. The real
      external identity-provider integration needs that provider's own configuration (issuer,
      JSON Web Key Set (JWKS) endpoint, client id), which this repository has no access to; a
      `StaticKeyAuthenticator` stands in behind the same `Authenticator` interface a real verifier
      would implement, so swapping it in later touches no caller.
- [x] Unit 10 — OpenTelemetry signals plus the four platform-specific metrics.
      `internal/platform/observability` builds the trace and metric providers (`Setup`, pre-existing)
      and now the structured logger too, via `NewLogger`: a `*slog.Logger` with a JSON handler, one
      record per line, using the standard library rather than a third-party logger or OpenTelemetry's
      own logging modules, per this item's own operational-simplicity bias. `cmd/server/main.go`
      sources its startup and shutdown logger from `NewLogger`, and `NewMux`
      (`internal/platform/connectserver`) wires its new `loggingInterceptor`
      (`internal/platform/connectserver/logging.go`) into every service's interceptor chain alongside
      the existing `otelconnect` interceptor, logging a request-boundary error at the same seam
      tracing already covers. All four named metrics are now emitted: the payload truncation count
      (`internal/delivery/payload/payload.go`, `citywalk.delivery.payload_truncation_count`) predates
      this pass; the membership reconciliation disagreement count
      (`internal/membership/batch/batch.go`,
      `citywalk.membership.reconciliation_disagreement_count`, by segment) is new — `Recompute`
      returned the disagreement in its `Report` before this pass, but nothing turned that figure into
      a metric; the suppression count by reason (`internal/platform/connectserver/delivery.go`,
      `citywalk.governance.suppression_count`) is also new, incremented in `checkProjectBudget`
      alongside the `project_budget` suppression event it already records — CW-0007's other suppression
      reasons remain unimplemented, so `project_budget` is the sole reason the counter carries so far;
      and the schema-version-skip count (`internal/delivery/payload/payload.go`,
      `citywalk.delivery.schema_version_skip_count`) is new as well, incremented where `buildEntry`
      finds no variant matching the channel's declared schema major.
      `internal/platform/observability/observability_test.go` proves the logger emits one parseable
      JSON record per line.
- [ ] Unit 11 — Phase one as a single process; log and columnar store in phase two.

## References

- [`docs/requirements.md`](../../docs/requirements.md) — the performance, availability, and security
  requirements this stack is sized and chosen against.
- [CW-0001](../CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md) — the
  service decomposition these components host.
- [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) — the synchronization
  protocol whose caches shape the delivery tier's memory requirements.
- [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) — the pipeline
  whose three consumers justify the log.
