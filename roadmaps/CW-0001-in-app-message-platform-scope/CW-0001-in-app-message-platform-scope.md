**English** · [日本語](CW-0001-in-app-message-platform-scope-ja.md)

# CW-0001 — In-app message platform: scope and decomposition

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0001](CW-0001-in-app-message-platform-scope.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **In progress** |
| Topic | Platform |
| Related | [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md), [CW-0010](../CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md) |
<!-- /CW-METADATA -->

## Introduction

We propose building an in-app message delivery platform in citywalk's server backend, and we
decompose the platform into five services along the axis that matters most: how fast the data each
service owns changes. A message definition changes when a campaign author saves it, an audience
membership changes hourly, and a trigger state changes several times a second. Those three rates
cannot share one storage engine or one deployment cadence without one of them dictating terms to the
others, so the decomposition puts each rate in its own service. This item fixes that decomposition
and names the roadmap item that designs each piece; the pieces themselves are designed in
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) through
[CW-0010](../CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md).

An **in-app message** is a message drawn over citywalk's own screens while a user is using the
application — a dialog, a banner, or a full-screen panel — as opposed to a push notification, which
arrives while the application is closed.

## Motivation

citywalk has no way to say anything to a user who is already in the application. Every message the
product wants to deliver today has to be either a push notification, which reaches a user at the
wrong moment and depends on a permission many users never grant, or a screen hard-coded into the
application, which ships on the application's release cycle and cannot be turned off without another
release. Neither can react to what the user is doing right now, and that is the whole point of the
messages the product wants to send: a walking route just finished, a feature the user has opened
three times but never completed, a maintenance window starting in an hour.

The cost of the hard-coded path is what makes this a platform problem rather than a feature request.
Each campaign built into the application is a code change, a review, a release, and a two-week wait
for adoption to reach most of the installed base — and it is still there, unchangeable, when the
campaign is over. A campaign whose copy turns out to be wrong cannot be fixed for a fortnight. A
campaign that should stop cannot stop. The product team ends up not asking for campaigns at all,
which is the failure mode that never shows up in a backlog.

A platform inverts that relationship: the application ships one renderer, and every campaign after
that is data. The engineering cost falls on the first campaign and stays flat afterward, and the
turnaround for a copy change or a stop drops from a release cycle to minutes. Getting that inversion
right is the contribution of this item, and the decomposition below is what keeps it right as the
platform grows.

## Detailed design

The platform is five services and one shared store per concern. We divide the work by the rate at
which its data changes, because two concerns with different change rates put incompatible demands on
storage — a definition read thousands of times per second and written once a day wants a cache, while
a counter written on every impression wants an in-memory store with expiry.

### Unit 1 — Definition service

The definition service owns everything a campaign author writes: messages, variants, triggers,
display conditions, schedules, governance policies, and segment definitions. It serves the
administrative application programming interface (API), validates a definition on save, keeps an
audit log of every change, and holds the kill switch that stops an active campaign.

Its data changes rarely and is read constantly, and correctness matters more than throughput here
than anywhere else in the platform: a malformed definition reaches every device. The service
therefore validates eagerly, rejects a contradiction at save time rather than at delivery time, and
treats a definition as immutable once published, publishing a new version instead of mutating one.
The schema those definitions follow is designed in
[CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md).

### Unit 2 — Audience service

The audience service answers one question: which messages is this channel eligible for? It evaluates
audience predicates, maintains segment membership, and resolves the eligible set for a channel on
demand. A **channel** is one installed application on one device, the unit the platform delivers to.

Membership changes on the order of minutes to hours — a user crosses a distance threshold, an
application update lands, a nightly recomputation finishes — which is slow enough to precompute and
cache, and fast enough that a daily batch would be stale. The predicate language and its evaluator
are designed in
[CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md), and the
membership index that makes the lookup cheap is designed in
[CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md).

### Unit 3 — Delivery service

The delivery service assembles the payload a device evaluates and serves it over the network. It
takes the eligible set from the audience service, selects the right variant per language, applies
the experiment and holdout assignment, enforces the payload size ceiling, and answers a conditional
request with no body when nothing has changed.

This service takes the platform's read traffic and nothing else, which is why it is separate from
the definition service that produces the data it serves: the two scale on unrelated inputs, and a
campaign author saving a definition must never contend with a million devices synchronizing. The
synchronization protocol is designed in
[CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md), and the assignment
arithmetic in
[CW-0008](../CW-0008-deterministic-experiment-assignment/CW-0008-deterministic-experiment-assignment.md).

### Unit 4 — Ingestion service

The ingestion service accepts event batches from devices, deduplicates them, and publishes them onto
a stream that the measurement pipeline and the audience service both consume. It guarantees
acceptance and durability, and nothing more: aggregation happens downstream, so a slow report never
slows a device.

Events arrive at the highest rate in the platform and matter individually the least, which inverts
the definition service's trade-off exactly. The service therefore optimizes for accepting a batch
cheaply and losing nothing after acceptance, and it rate-limits per channel so that one
misbehaving device cannot degrade the rest. The pipeline is designed in
[CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md).

### Unit 5 — Governance

Governance decides which of several displayable messages a user actually sees, and how often. It
spans the device and the server rather than living in one service: per-message caps and priority
resolution run on the device, where the state is, while project-wide caps and the optional
pre-display confirmation run on the server, where the cross-device view is.

Splitting one concern across the boundary needs a stated rule, or the two halves drift. The rule is
that the device holds authority and the server holds the exception: a campaign gets device-side
governance by default, and opts into server confirmation only when strictness is worth a round trip.
Both halves are designed in
[CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md).

### The delivery boundary

The five services sit on one architectural decision that
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) makes: the server
resolves the audience, and the device evaluates the triggers. Every unit above is shaped by that
split, which is why it is a separate item rather than a paragraph here.

## Alternatives considered

- **Build each campaign into the application.** Rejected. Every campaign becomes a release, and
  adoption of a release takes weeks, so a campaign cannot be corrected or stopped within its own
  lifetime. The engineering cost also grows with the number of campaigns instead of staying flat,
  which is the property that decides whether the product team asks for campaigns at all.
- **Adopt a third-party messaging vendor.** Rejected for this platform, on data residency and on
  coupling. citywalk's targeting signals are location-derived, and exporting a user's movement
  history to a vendor to segment on it widens the privacy surface well past what displaying a dialog
  justifies. A vendor also fixes the trigger vocabulary, and the walking-specific triggers this
  application will want are exactly the ones a general vendor does not model.
- **One service instead of five.** Rejected, though it is the right starting shape for a prototype.
  A single service forces one storage engine on data whose change rates differ by four orders of
  magnitude, and forces one deployment cadence on a definition path that changes weekly and an
  ingestion path that must not be redeployed during peak. The five services can still run as one
  process early on; the boundary that matters is the one in the code.
- **Decompose by entity rather than by change rate.** Rejected. An entity-shaped split — a message
  service, a user service, a segment service — cuts across the read path, so serving one payload
  touches every service. The change-rate split keeps the hot read path inside one service.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [ ] Unit 1 — Definition service: schema, administrative API, validation, audit log, kill switch.
      CW-0003's schema, save-time validation, and a minimal insert/get store exist and are tested.
      The kill switch and its audit log are also implemented and tested now: `store.UpdateState`
      moves a message between states only along FR-MSG-01's forward-only transitions
      (`MessageState.CanTransition`, itself newly tested), and records every successful move in
      `message_audit_log`, which `store.ListAuditLog` reads back. Still missing: a network-facing
      administrative API (authentication, and the rest of the create/update/delete/publish surface —
      today's store package is a persistence layer a future API would call, not the API itself).
- [ ] Unit 2 — Audience service: predicate evaluation and segment membership.
      CW-0004's predicate language and evaluator and CW-0005's membership index are both implemented
      and tested; a channel's eligible set resolves end to end through the reverse index CW-0002's
      payload assembly reads. Left unchecked because neither CW-0004 nor CW-0005 itself is marked
      Implemented yet (each still carries its own open sub-items) — this box mirrors theirs rather
      than reaching a more finished-sounding state on its own account.
- [ ] Unit 3 — Delivery service: payload assembly, variant selection, synchronization.
      CW-0002's payload assembly, CW-0006's delta synchronization protocol, and CW-0008's experiment
      and holdout assignment all work end to end against real Postgres and Redis: variant selection
      is by language first, then by deterministic assignment among that language's variants, and a
      message whose holdout claims a channel is excluded from its payload. Left unchecked because
      CW-0008 itself still has open units (the display-time pin and holdout-qualification telemetry
      are permanently or currently out of reach — see its own progress notes), so this box mirrors
      that rather than reaching a more finished-sounding state on its own account.
- [ ] Unit 4 — Ingestion service: event acceptance, deduplication, and publication.
      CW-0009's acceptance, per-channel rate limiting, identifier-based deduplication, and rollup
      consumption are implemented and tested. "Publishes them onto a stream" is phase-one Postgres
      (documented in `migrations/0006_events.sql`), not a real stream a second consumer could scale
      across independently — CW-0009's own Unit 3 note covers this in detail.
- [ ] Unit 5 — Governance: device-side caps and priority, server-side project caps and confirmation.
      The server-side half — CW-0007's project-wide budget, its atomic strict-confirmation decrement,
      and suppression telemetry — is implemented and tested. The device-side half (per-message caps,
      priority resolution, candidate lifecycle) is permanently out of scope for this repository, per
      `docs/requirements.md`; this unit's checkbox can therefore never fully close from server-side
      work alone, which is stated here rather than left to look like an oversight.

## References

- [`docs/requirements.md`](../../docs/requirements.md) — the requirement catalog this decomposition
  satisfies, with an identifier per requirement.
- [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) — the delivery
  boundary every unit above is shaped by.
- [CW-0010](../CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md) — the runtime,
  the storage engines, and the load estimate the five services are sized against.
