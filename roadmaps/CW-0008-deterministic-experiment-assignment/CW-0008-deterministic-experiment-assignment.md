**English** · [日本語](CW-0008-deterministic-experiment-assignment-ja.md)

# CW-0008 — Deterministic experiment and holdout assignment

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0008](CW-0008-deterministic-experiment-assignment.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **In progress** |
| Topic | Targeting |
| Related | [CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md), [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) |
<!-- /CW-METADATA -->

## Introduction

We propose assigning experiment variants and holdouts by computation rather than by storage. An
assignment is the result of hashing a stable identity together with a per-experiment salt into one of
ten thousand buckets, and looking that bucket up in an ordered range table that maps bucket ranges to
variants, with one range reserved for the holdout. Nothing is written: the same identity and the same
experiment yield the same bucket on any server, on any device, at any time, including after a
database restore.

Assignment is one of the few places in the platform where the server and the device must agree
without communicating — the server picks a variant while assembling a payload, and reporting must
attribute an impression to the same variant weeks later. A pure function of inputs both sides hold is
the only construction that gives that agreement for free.

## Motivation

The obvious design stores assignments: when a user first qualifies for an experiment, draw a random
variant and write the pair to a table. It is easy to reason about and it is what most first
implementations do, and it accumulates three costs that are hard to see at the start.

The first is volume. Ten million users across the experiments a busy project runs is a table that
grows without bound and is read on every payload assembly, which puts a row lookup on the hottest
path in the platform for information that could have been computed in nanoseconds. The second is
fragility: the assignment table becomes state that must be backed up, restored consistently with the
rest of the platform, and migrated when the experiment definition changes — and a restore that loses
it silently re-randomizes every participant, which invalidates the experiment without failing
anything. The third is the split brain: the device must know the variant to render it, so either the
server sends the assignment with every payload, or the two sides disagree.

Re-drawing at each impression is the other naive option and it is simply wrong. An experiment
measures the difference between what two groups of users experienced, so a user who sees arm A on
Monday and arm B on Tuesday belongs to neither group and contaminates both.

Computing the assignment removes all four problems at once, and the remaining work is making the
computation correct: uniform, independent across experiments, stable under weight changes, and
well-defined when a user's identity changes at login. Those four are what the design below covers.

## Detailed design

### Unit 1 — The identity and the login transition

Assignment hashes the user identifier when the channel is associated with an account, and the channel
identifier otherwise. Preferring the user identifier is what makes an experiment consistent across a
user's phone and tablet, which is the property a per-device identity cannot offer.

A login changes the identity, and therefore potentially the assignment, mid-experiment. The device
handles this by pinning: once a message has been displayed, the variant used is recorded alongside
the impression and reused for that message thereafter, so a user who logged in after seeing arm A
keeps seeing arm A. Analysis reads the variant from the impression record rather than recomputing it,
which makes the pin authoritative for reporting as well.

### Unit 2 — The bucketing function

The bucket is the 64-bit hash of the experiment's salt concatenated with a separator and the
identity, taken modulo ten thousand. Ten thousand buckets fix the granularity of a split at one
hundredth of a percent, which is finer than any split a campaign author writes and coarse enough to
keep the range table small.

The salt is generated per experiment and never reused. Without it, every experiment would bucket the
same identity identically, so the users in arm A of one experiment would be exactly the users in arm A
of the next; correlated exposure across experiments then makes each experiment's result depend on the
others', which is the failure that a shared salt causes and that no analysis can undo afterward.

The hash is a fast non-cryptographic 64-bit function, because the requirement is uniformity, not
unpredictability. A cryptographic hash would satisfy uniformity too, at a throughput cost paid on
every payload assembly; the one property it adds — resistance to an adversary searching for an
identity that lands in a chosen bucket — protects nothing here, since a user who forces themselves
into a variant of citywalk's own dialog gains nothing.

### Unit 3 — The range table and reweighting

Each experiment stores an ordered table mapping bucket ranges to variants: buckets 0 through 4,999 to
variant A, 5,000 through 9,999 to variant B. Assignment is a lookup, not arithmetic over the weights.

Storing ranges rather than recomputing from weights is what makes a mid-flight weight change safe. A
computed split moves a bucket whenever any weight changes, so shifting a split from 50/50 to 60/40
would move users out of the arm they were already measured in. With an explicit table, the reweight
computes a new table that keeps every bucket it can and moves only the buckets the new proportions
require, and the change is recorded with a timestamp so that analysis can exclude the affected
buckets from a comparison that spans the change.

### Unit 4 — Holdouts

A campaign's holdout is a reserved range in the same table. A holdout of five percent reserves five
hundred buckets, and a channel landing there is eligible for the campaign in every respect but
receives no content.

A project-wide control group uses a separate salt fixed per project rather than per experiment, so a
user in it is held out of every campaign consistently. Keeping the two mechanisms separate matters
because they answer different questions — one measures a campaign against its own absence, the other
measures the platform against its absence — and merging them would make a user's exclusion from one
campaign depend on an unrelated project-level decision.

### Unit 5 — Reporting the counterfactual

A held-out channel emits an event recording that it qualified and was withheld, carrying the campaign
and the reason. Without that event, a holdout produces no data at all, and the comparison it exists
for — what these users did without the message — has no denominator.

The same reasoning applies to the variant recorded on an impression: reporting attributes an
impression to the variant stored on it, never to a recomputation, so a later reweight cannot
retroactively move historical impressions between arms.

### Unit 6 — Verification

Two properties are checked automatically rather than assumed. A uniformity test runs the bucketing
function over a large synthetic identity population and fails when the bucket distribution deviates
beyond a threshold, which catches a salt concatenation that accidentally correlates with the identity
format. A parity test asserts that the server implementation and the device implementation produce
identical buckets for a shared fixture set, which catches the divergence that two independent
implementations of the same arithmetic otherwise develop — a differing string encoding, or a signed
modulo on a negative hash.

## Alternatives considered

- **A stored assignment table.** Rejected on volume, fragility, and agreement. It puts a row lookup
  on the hottest path for a value that is computable, becomes state that a restore can silently
  re-randomize, and still requires shipping the assignment to the device to render it.
- **Re-drawing the variant at each impression.** Rejected as incorrect. A user exposed to both arms
  belongs to neither group, so the measurement the experiment exists to produce is contaminated
  rather than merely noisy.
- **One salt shared by every experiment.** Rejected. It correlates arm membership across
  experiments, so results become mutually dependent in a way no later analysis can separate.
- **A consistent hashing ring instead of fixed buckets.** Rejected as a mismatch. A ring minimizes
  movement when the number of *bins* changes, which is what a sharding scheme needs; an experiment
  changes the *proportions* over a fixed set of variants, which the explicit range table handles
  directly and more legibly.
- **Sequential or round-robin assignment.** Rejected. It requires a shared counter, which reinstates
  the stored state this design removes, and it correlates the assignment with registration order —
  so an arm can be systematically older than another.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [ ] Unit 1 — Identity selection and the display-time pin that survives a login.
      Identity selection is implemented, tested, and now live on the delivery path:
      `internal/delivery/payload.applyOverlay` (via `assignVariant`, CW-0006 Unit 4's per-channel
      overlay step — this function was `buildEntry` before that unit split payload assembly into a
      shared bundle and a per-channel overlay) calls `assign.Assign` at payload assembly time (per
      language group, with a bucket allocation derived from CW-0003's validated variant weights) and
      the resulting variant_id is what a real impression now carries, aggregated by CW-0009 into
      `campaign_rollup` and read back by `internal/event/report.Variants`. Because this schema has no
      user-account linkage yet, the identity used today is always the channel identifier — never a
      user identifier — which the design's "preferring the user identifier" property needs that
      linkage to actually deliver; this is a documented simplification, not a deviation once user
      accounts exist. The one piece permanently unbuilt in this repository is the pin itself, a client
      SDK behavior.
- [x] Unit 2 — The salted bucketing function over ten thousand buckets.
- [x] Unit 3 — The explicit range table and a reweight that moves the fewest buckets.
- [x] Unit 4 — Campaign holdouts as reserved ranges, and the project-wide control group.
      The reserved-range mechanism is also wired into the live delivery path now: a message's
      `HoldoutFraction` reserves a real range in the range table `payload.assignVariant` (called from
      `applyOverlay`) builds, and a channel landing there is excluded from its payload exactly like a
      channel that never qualified.
      `InProjectHoldout` (the separate, project-wide control group) has no caller yet — there is no
      project entity in this schema for it to scope against.
- [x] Unit 5 — Holdout qualification events, and reporting from the recorded variant.
      Reporting from the recorded variant is done (see Unit 1's note). Holdout qualification events
      are now wired into the same live delivery path. `internal/delivery/payload.applyOverlay` calls
      `recordHoldoutQualified` whenever a channel lands in a message's own holdout.
      `recordHoldoutQualified` writes a `model.KindHoldoutQualified` event through
      `internal/event/ingest.Record`. That is the same server-emits-an-event-as-a-side-effect pattern
      `checkProjectBudget` (`internal/platform/connectserver/delivery.go`) uses for CW-0007's
      `project_budget` suppression. Both fail the caller rather than discard a write error.
      `internal/event/attribution` already reads these events as exposures under `HoldoutVariantID`.
      `TestHoldoutQualifiedEventFromBuildIsCountedByAttribution`
      (`internal/delivery/payload/payload_integration_test.go`) proves the chain against real Postgres
      and Redis. `payload.Build` excludes the channel, leaves the event behind, and a later conversion
      attributes to the holdout. `InProjectHoldout`'s project-wide holdout still has no caller, same
      as Unit 4's note. This schema still has no project entity for it to scope against, and adding
      one is a separate, larger change than this unit.
- [x] Unit 6 — Automated uniformity and server-to-device parity tests.
      The uniformity test and the fixture parity table are both implemented — the fixture is the
      server-side artifact a device implementation would be checked against, since no device
      implementation exists in this repository to test against directly.

## References

- [CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md) — the variant
  entity this assignment selects among, and where the weights are stored.
- [CW-0009](../CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) — the
  pipeline that reads the recorded variant and the holdout events to produce comparisons.
- [`docs/requirements.md`](../../docs/requirements.md) — the experimentation requirements this item
  satisfies, including assignment stability and mid-flight reweighting.
