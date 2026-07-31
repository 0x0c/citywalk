**English** · [日本語](CW-0007-display-governance-ja.md)

# CW-0007 — Display governance: caps, priority, and conflict resolution

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0007](CW-0007-display-governance.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **Proposal** |
| Topic | Display governance |
| Related | [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md), [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) |
<!-- /CW-METADATA -->

## Introduction

We propose splitting display governance between the device and the server, with the device
authoritative and the server advisory. The device enforces per-message caps, resolves priority among
simultaneous candidates, and serializes display through a single slot with a cooldown; the server
issues a project-wide impression budget that the device spends locally and the server reconciles from
reported impressions, using an approximate sliding-window counter. A campaign that cannot tolerate
the approximation opts into the confirmation endpoint from
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) and pays a round trip
for exactness.

Every suppression is reported as an event carrying its reason. That is a governance requirement
rather than a telemetry nicety, and the *Motivation* explains why.

## Motivation

Governance is what stands between a platform that works and a platform the product team stops using.
Once campaigns are cheap to create, they multiply, and their triggers overlap: a user finishing a
walking route at the end of a session can plausibly qualify for a route-summary prompt, a review
request, a subscription offer, and a maintenance notice at the same instant. Without governance, the
user sees four dialogs in a row and learns to dismiss anything the application shows. The damage is
not to one campaign but to every future one.

Deciding where to enforce is harder than deciding to enforce, because neither side holds everything
the decision needs. Only the device knows the timing facts — what is on screen, what was displayed
eleven minutes ago, whether the user just dismissed something — and only the device can act without a
network. Only the server holds the cross-device view, so a user reading on a phone and a tablet is
one user to the server and two to any device-local counter. Choosing one side wholesale gives up
either offline correctness or cross-device accuracy.

The resolution is to notice that the two demands differ in how much precision they need. A
per-message cap is a promise to a user about a specific campaign and must hold exactly, and it
happens to be exactly what the device can enforce alone. A project-wide cap is a comfort threshold,
and one impression of drift across a user's second device changes nothing anyone would notice. So the
device enforces what must be exact, and the server approximates what tolerates approximation — with
an escape hatch for the campaign whose economics make the approximation unacceptable.

An unreported suppression makes all of this undebuggable. A campaign suppressed by a cap and a
campaign nobody qualified for produce identical reports — zero impressions — and the first is fixed
by changing a cap while the second is fixed by changing an audience. Without the reason, the campaign
owner cannot tell which one they have.

## Detailed design

### Unit 1 — Per-message caps on the device

The device persists, per message, the impression count and the last impression time, and enforces the
message's total cap and minimum interval before displaying. The state survives application restarts
and is keyed by message identifier, so an edit to a campaign does not reset its history.

Interval arithmetic uses the device's monotonic clock anchored against server time rather than the
wall clock, for the reason
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) gives about expiry:
moving the wall clock forward must not make a message eligible again. A device whose offset against
server time exceeds a threshold reports the offset, since a large drift is either a misconfigured
device or a deliberate one, and both are worth seeing.

### Unit 2 — Priority and the single display slot

When several messages become displayable at once, the device displays exactly one. Selection takes
the highest priority; ties break by the earliest campaign creation time and then by message
identifier, which makes the choice deterministic across devices and reproducible in a bug report. A
random tiebreak would make the same situation resolve differently on two devices, which is
untestable.

The display slot holds one message. A message arriving while the slot is occupied does not queue
behind it, and after a message closes, a cooldown of a configurable duration suppresses the next one,
so that two campaigns cannot present as a sequence of dialogs.

### Unit 3 — Candidate lifecycle

A candidate that could not display when its trigger fired is discarded by default rather than held.
Holding is available per campaign but is not the default, because a held candidate surfaces later at
a moment that no longer matches the context its trigger described — a route-completion prompt
appearing on the settings screen an hour later reads as a malfunction.

A candidate waiting out a display delay is cancellable: when a cancellation trigger's event occurs
before the delay elapses, the candidate is dropped. Cancellation is what makes a delayed campaign
safe to author, since it lets the author say "offer help after 30 seconds on this screen, unless the
user succeeds first".

### Unit 4 — The project-wide cap

The server maintains a per-user impression count over a rolling window using an approximate
sliding-window counter: two fixed buckets, the current one and the previous one, with the estimate
being the current count plus the previous count weighted by the fraction of the window the previous
bucket still covers.

The approximation is chosen against three alternatives on the cost of exactness. A fixed window is
cheaper still but permits a burst of twice the cap across a boundary, which is precisely the
experience the cap exists to prevent. An exact sliding log stores a timestamp per impression, which
is unbounded per user and holds data the platform has no other use for. A token bucket is bounded and
smooth but expresses a rate rather than a count, so it cannot state "two per day" — the form the cap
is authored in. The two-bucket estimate is bounded in memory, cannot be gamed across a boundary, and
its error is a known function of where in the window the request lands.

The counter lives in the in-memory store and updates through a single atomic script, so that a
concurrent read and increment from two servers cannot both observe room for the last impression.

### Unit 5 — Reconciling device and server

The server issues, in each payload, the remaining project-wide budget for the current window as of
assembly. The device decrements it locally on each impression and stops displaying non-exempt
campaigns at zero. Reported impressions update the server's counter, and the next payload carries a
corrected budget.

The consequence to state plainly is that between synchronizations a user's second device spends the
same budget, so a user with two devices can exceed the cap by up to the budget carried on one of
them. That is the accuracy this design trades away, it is bounded by the budget size, and a campaign
that cannot accept it uses Unit 6.

Exempt campaigns bypass the budget entirely. Exemption is a per-campaign flag for messages that must
reach the user regardless of comfort thresholds, such as a service interruption notice.

### Unit 6 — Strict enforcement and suppression telemetry

A campaign marked strict calls the confirmation endpoint immediately before display. The endpoint
decrements the authoritative counter and answers in one atomic operation, so two devices cannot both
receive the last remaining impression, and it fails closed on a timeout.

Every suppression emits an event naming the message and one of a closed set of reasons: the
per-message cap, the minimum interval, the project-wide budget, losing on priority, a display
condition not met, the cooldown, a cancellation trigger, or expiry. The set is closed rather than
free-text so that the reasons aggregate into the report described in
[CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md); a free-text
reason produces a report nobody can group.

## Alternatives considered

- **Enforce everything on the server.** Rejected. Every governance decision would then need a round
  trip at display time, which reintroduces the latency and offline failure
  [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) rejects, for a
  guarantee that only the project-wide cap actually needs.
- **Enforce everything on the device.** Rejected. A per-device counter treats one user with a phone
  and a tablet as two users, so a project-wide cap of two per day becomes four for exactly the users
  most engaged with the application — the ones most exposed to the over-messaging the cap prevents.
- **An exact sliding log for the project-wide cap.** Rejected on cost and on data minimization. It
  stores a timestamp per impression per user, unbounded in the number of impressions, to buy
  precision that no threshold in this domain is sensitive to.
- **A token bucket for the project-wide cap.** Rejected on expressiveness. A bucket models a rate
  with a burst allowance, and campaign authors write caps as counts per calendar period; translating
  between the two would make the configured number and the observed behavior disagree.
- **A distributed consensus counter for exactness everywhere.** Rejected. It would make every cap
  exact across devices, at the cost of a coordinated write on the display path for every campaign —
  paying the strict-mode price universally to benefit the few campaigns that need it.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [ ] Unit 1 — Device-side per-message counters on a monotonic clock, with drift reporting.
- [ ] Unit 2 — Deterministic priority resolution, the single display slot, and the cooldown.
- [ ] Unit 3 — Candidate lifecycle: discard by default, optional hold, cancellation triggers.
- [ ] Unit 4 — Approximate sliding-window counter for the project-wide cap, updated atomically.
- [ ] Unit 5 — Budget issuance in the payload, device-side spending, and server reconciliation.
- [ ] Unit 6 — The strict confirmation endpoint and closed-set suppression telemetry.

## References

- [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) — the delivery
  boundary this split follows, and the confirmation endpoint strict mode uses.
- [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) — the
  pipeline that turns suppression events into the reason breakdown a campaign owner reads.
- [`docs/requirements.md`](../../docs/requirements.md) — the governance requirements this item
  satisfies, including the caps, the single-message rule, and the cooldown.
