**English** · [日本語](CW-0002-hybrid-delivery-model-ja.md)

# CW-0002 — Hybrid delivery: server-resolved audience, device-evaluated triggers

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0002](CW-0002-hybrid-delivery-model.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **Proposal** |
| Topic | Delivery model |
| Related | [CW-0001](../CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md), [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md), [CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md) |
<!-- /CW-METADATA -->

## Introduction

We propose a hybrid delivery model for citywalk's in-app messages: the server decides **who** is
eligible, the device decides **when** to display, and a campaign that needs strictness may opt into
one server confirmation immediately before it displays. The server resolves audience predicates and
sends each device only the message definitions that device is already eligible for; the device
evaluates triggers, display conditions, frequency caps, and priority locally against state it alone
holds. This item fixes that boundary and the contract on each side of it.

The split is not a compromise between two designs — it follows from a property of the conditions
themselves. An audience condition ("has walked more than 50 kilometers") changes over hours and may
encode information we do not want on the device. A trigger condition ("the route-completed screen
just appeared for the third time") changes by the second and describes state that exists nowhere but
the device. Put each condition where its data already lives, and the boundary draws itself.

## Motivation

Two designs suggest themselves before the hybrid one, and understanding why each fails is what
justifies the extra machinery the hybrid model needs.

The first is **server-side decisioning**: on every screen transition, the application asks the server
what to display. The design is appealing because all the logic stays in one place, changes take
effect instantly, and the device learns nothing it does not need. It fails on latency and on volume,
and the volume figure is the one that settles it. At 10 million monthly active users with a quarter
of them active on a given day and roughly two and a half sessions each, citywalk sees about 6 million
sessions a day; at a dozen screen transitions per session, per-transition decisioning is 75 million
requests a day, near 900 per second on average and several thousand at peak — and every one of them
sits between a user's tap and the next screen. A screen transition that waits on a network round trip
is a slower application in exchange for a dialog that usually does not appear. Worse, none of it
works on a subway platform, and a walking application spends a lot of its time where the signal is
poor.

The second is **pushing every rule to the device**: send the full campaign catalog, audience
predicates included, and let the device work out both who and when. The volume problem disappears —
synchronizing once a session is 6 million requests a day, about 70 per second, most of which return
no body. Two other problems replace it. A payload that carries audience predicates carries them to an
attacker too, since anyone can read what their own device received; a predicate naming an internal
propensity score or a contract tier is then public. And the device cannot evaluate what it cannot
see: a predicate over server-side aggregates, or over another device belonging to the same user, has
no local answer.

The hybrid model takes the volume profile of the second design and the confidentiality of the first,
because the two failures land on opposite halves of the condition space. Nothing about "who" needs to
be on the device, and nothing about "when" can be anywhere else.

## Detailed design

### Unit 1 — The boundary rule

Every condition in a message definition belongs to exactly one side, decided by a rule that admits no
per-campaign discretion.

The **server** owns any condition over data the server holds: attributes, tags, segment membership,
event aggregates over history, experiment and holdout assignment, and the campaign's delivery window.
The server evaluates these while assembling a payload, and the result reaching the device is a plain
fact — this message is eligible — with the reasoning stripped.

The **device** owns any condition over data the device holds: which event just occurred and how many
times, which screen is showing, how long the session has run, when this message was last displayed
and how often, and which message is on screen now. These arrive on the device as declarative
definitions, since they describe device state and reveal nothing about the audience.

The rule generalizes: a condition goes to whichever side already holds the data it reads. Where a
condition could read either — the application version, which the server knows from registration and
the device knows directly — it goes to the server, because the server's copy is the one the audience
count was computed from, and a report that disagrees with the delivery is worse than a slightly
stale predicate.

### Unit 2 — The payload contract

The payload is a list of eligible message definitions. Each entry carries the message identifier and
version, the selected variant's content, the trigger and display-condition definitions, the
governance policy (caps, priority, exemptions), and an absolute expiry timestamp. It carries no
audience predicate, no segment identifier, no attribute value, and no other user's data.

Two properties of the payload are load-bearing. It is **complete**: the device never needs to ask a
follow-up question to decide whether to display, which is what makes the display decision cost zero
network hops. And it is **self-expiring**: each entry states its own expiry as an absolute time, so a
device that has not synchronized in a week still stops displaying a campaign that ended yesterday.
Expiry is judged against server time plus the device's monotonic clock rather than the device's wall
clock, so moving the clock forward does not resurrect an expired message.

The payload has a size ceiling. When the eligible set exceeds it, the delivery service truncates in
priority order and records the truncation as a metric, because a silently truncated payload looks
exactly like a campaign that nobody qualified for.

### Unit 3 — Freshness and the kill switch

The device synchronizes on a schedule the server dictates: each payload carries the earliest time the
device should next synchronize, and the device also synchronizes on a foreground return once that
time has passed. Putting the interval in the payload rather than in the software development kit
(SDK) lets the server widen it under load and narrow it during a campaign launch, without shipping
an application update.

A campaign stop must not wait for that interval. Stopping sets the campaign's state in the definition
service and invalidates the cached payloads that contain it, so the next synchronization from any
device drops the campaign; the reachable target is therefore one synchronization interval, and the
platform states that plainly rather than promising instant removal it cannot deliver. A campaign that
genuinely cannot tolerate a stale window is a campaign for Unit 4.

Synchronization is jittered. Without jitter, a fifteen-minute interval turns a million devices into a
thundering herd every fifteen minutes; the server therefore returns the next synchronization time
with a random offset spread across a fraction of the interval.

### Unit 4 — The server confirmation escape hatch

A campaign may set a flag that makes the device call one lightweight endpoint immediately before it
displays. The endpoint answers yes or no, and the device displays only on an explicit yes: a timeout
or an error suppresses the display rather than allowing it.

This escape hatch exists for the cases the payload model genuinely cannot serve — issuing a coupon
against a fixed budget, enforcing a cap that must hold across a user's devices, or gating on stock
that changed a second ago. Restraint is the point: every campaign that sets the flag reintroduces the
latency and the offline failure the model was built to avoid, so the flag is a per-campaign exception
that the administrative interface makes visible, not a default.

### Unit 5 — Degradation

The model's failure behavior follows from the payload being complete and self-expiring. When the
platform is unreachable, the device keeps evaluating its cached payload, so campaigns already
delivered keep working and only changes stop propagating. When the device is offline, triggers
evaluate normally and measurement events accumulate locally for later submission, bounded by a
device-side ceiling that drops the oldest events first.

Both behaviors are degradations rather than outages, and stating which is which matters: a platform
outage costs freshness, not display. The one thing that does stop is a server-confirmed campaign,
which fails closed by design.

## Alternatives considered

- **Server-side decisioning on every screen transition.** Rejected on latency and volume, as the
  *Motivation* sets out: it puts a network round trip in front of a screen transition, multiplies
  request volume by more than tenfold against the synchronization model, and stops working entirely
  when the device is offline.
- **Pushing the full rule catalog, audience predicates included.** Rejected on confidentiality and
  on capability. A device receives what an attacker can read, so a predicate over an internal score
  becomes public, and a predicate over server-side aggregates or a sibling device has no local
  answer at all.
- **Invalidating caches with a silent push notification.** Rejected as a dependency, though it is
  worth adding later as an optimization. Silent delivery is unreliable on both platforms and is
  throttled by the operating system, so a correctness property that rested on it would fail
  intermittently and undebuggably. The synchronization interval must be short enough on its own; a
  silent push can then narrow the window opportunistically.
- **Prefetching per screen instead of per session.** Rejected. Fetching the messages for the screen
  the user is about to open reduces payload size, but it reinstates the per-transition round trip
  that Unit 1 exists to remove, and it leaks the user's navigation to the server at a granularity
  the platform has no need for.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [ ] Unit 1 — The boundary rule, encoded so that a definition cannot place a condition on the wrong side.
- [ ] Unit 2 — The payload contract: complete, self-expiring, size-capped, audience-free.
- [ ] Unit 3 — Server-dictated synchronization interval, jitter, and the kill switch.
- [ ] Unit 4 — The server confirmation endpoint, failing closed, visible per campaign.
- [ ] Unit 5 — Degradation behavior for a platform outage and for an offline device.

## References

- [`docs/requirements.md`](../../docs/requirements.md) — the requirements this model satisfies,
  notably the zero-round-trip display decision and offline operation.
- [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) — the synchronization
  protocol that carries the payload contract defined here.
- [CW-0007](../CW-0007-display-governance/CW-0007-display-governance.md) — the governance split that
  follows this boundary, and the caps the server confirmation endpoint enforces.
