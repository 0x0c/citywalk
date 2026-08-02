**English** · [日本語](CW-0006-payload-delta-sync-ja.md)

# CW-0006 — Delivery payload synchronization

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0006](CW-0006-payload-delta-sync.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **Implemented** |
| Topic | Delivery model |
| Related | [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md), [CW-0003](../CW-0003-message-definition-schema/CW-0003-message-definition-schema.md), [CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md) |
<!-- /CW-METADATA -->

## Introduction

We propose the protocol by which a device keeps its delivery payload current: a content-addressed
entity tag over the payload's contents, a conditional request that returns no body when that tag
still matches, an optional cursor-based delta for the cases where the payload is large and the change
is small, and a two-tier cache that lets thousands of devices share the expensive half of assembling
a payload. The server dictates the synchronization interval and jitters it, and the device backs off
with full jitter on error.

The protocol's design goal is that the common case costs almost nothing. Most synchronizations happen
when nothing has changed, so the path that answers "nothing has changed" is the path worth
optimizing, and every other mode in this item exists to keep that path short.

## Motivation

At citywalk's target scale the platform serves roughly 6 million synchronizations a day, and the
overwhelming majority of them find a payload identical to the one the device already holds. Campaign
definitions change a few times a day; devices synchronize a few times per device per day. The ratio
is what shapes the protocol: the interesting question is not how to transfer a payload efficiently
but how to avoid transferring one at all.

Sending the full payload every time is what a first implementation does, and its cost is easy to
underestimate because the payload is small. A payload of fifty kilobytes across 6 million
synchronizations is 300 gigabytes a day of egress that conveys nothing, and on the device it is
mobile data spent by a user who did not ask for it. A walking application is used outdoors, on
cellular, by users who notice.

Assembly cost is the second half of the problem, and it is the half that a conditional request alone
does not solve. Answering "has anything changed for this channel?" still requires knowing which
messages this channel is eligible for, which means reading its segment membership and evaluating the
remaining predicates. Doing that per request, 6 million times a day, repeats work that is identical
across every device with the same segments, locale, and application version — and there are far fewer
distinct combinations of those than there are devices.

## Detailed design

### Unit 1 — Payload identity

The server computes an entity tag for a channel's payload as a hash over the sorted list of the
elements that determine what the device would render: each eligible message's identifier and version,
the selected variant's identifier, the governance policy version, and the schema version being
emitted. The hash is a non-cryptographic 64-bit function, since the tag guards against staleness
rather than against forgery.

Deriving the tag from contents rather than from a timestamp is what makes it correct under
concurrent edits. A timestamp-based tag changes when an unrelated campaign is saved, forcing every
device to re-download an unchanged payload; a content-derived tag changes exactly when the rendered
result would differ, so an edit that ends up not affecting a given channel costs that channel
nothing.

### Unit 2 — The conditional request

The device sends its stored tag with each synchronization. When the tag matches, the server responds
with a status indicating no change and no body; the requirement budget for that response is 50
milliseconds at the 99th percentile, against 200 for a full one.

Meeting that budget means the no-change path must not perform the full assembly. The server therefore
caches the computed tag per channel with a short expiry, so a repeat synchronization inside the
expiry window is a single lookup and a comparison. A cache miss falls through to assembly, which
computes both the tag and the body, and answers with whichever the comparison calls for.

### Unit 3 — Delta mode

When the payload is large and only part of it changed, sending the whole payload wastes most of the
transfer. The device may therefore send the version cursor it last applied, and the server responds
with the entries added or changed since that cursor plus tombstones for the entries removed.

Every delta protocol needs a stated answer for a cursor the server can no longer honor, or the device
and the server drift apart silently. The server retains a bounded change log; a cursor older than the
log, or one the server does not recognize, produces a full payload and a fresh cursor rather than an
error. Falling back to correctness is always available, which is what keeps the delta path an
optimization rather than a second source of truth.

Delta mode is off by default. A payload under the size where the transfer matters costs more in
protocol complexity than it saves in bytes, so the server enables the mode per project once the
measured payload size justifies it.

### Unit 4 — Two-tier assembly cache

Payload assembly splits into a part shared across devices and a part specific to one. The shared part
— which messages are eligible, and what their content is — depends only on the channel's segment
membership, locale, platform, application version, and supported schema version. The specific part is
the experiment and holdout assignment and the server-side governance state.

The server therefore caches a **bundle** keyed by the shared inputs, with the segment membership
represented by a hash of the channel's membership bitmap from
[CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md), and applies the
per-channel overlay on top of a cache hit. Because those inputs take far fewer distinct values than
there are channels, one bundle serves many devices, and assembly cost falls from per-request to
per-distinct-combination.

A bundle is invalidated by campaign edits, not by time, so a change reaches devices at their next
synchronization rather than after a cache expiry. The invalidation is keyed by campaign, which means
an edit invalidates only the bundles containing that campaign.

### Unit 5 — Scheduling and backoff

Each response carries the earliest time the device should synchronize again. The server computes the
base interval and adds a random offset spread across a fraction of it, so that a million devices that
synchronized together at a release do not return together fifteen minutes later. The device also
synchronizes on a foreground return once the stated time has passed, which is what makes a session
that starts after a long gap see current campaigns.

On an error or a timeout the device backs off exponentially with full jitter, choosing its delay
uniformly at random from the interval up to the current bound rather than at the bound. Retrying at
the bound synchronizes the retries of every device that failed together, which is exactly the herd
the backoff exists to break up during a partial outage.

### Unit 6 — Size and compression

Responses are compressed, and the payload has a size ceiling measured after compression. When the
eligible set exceeds the ceiling, the delivery service drops entries in priority order, lowest
first, and emits a metric naming the channel cohort and the number dropped.

The metric is the deliverable here as much as the truncation is. A truncated payload behaves exactly
like a payload nobody qualified for, so without the metric a project that has quietly outgrown the
ceiling looks like a project whose campaigns are underperforming.

## Alternatives considered

- **Send the full payload on every synchronization.** Rejected on bandwidth and on assembly cost.
  The transfer conveys nothing in the overwhelming majority of cases, and it spends a user's cellular
  data outdoors, which is where a walking application is used.
- **Invalidate device caches with a silent push notification instead of polling.** Rejected as the
  primary mechanism, for the reason
  [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) gives: silent
  delivery is throttled and unreliable on both platforms, so freshness resting on it would fail
  intermittently. It is worth adding later as an accelerator on top of polling.
- **One endpoint per message instead of one payload.** Rejected. Fetching each eligible campaign
  separately makes the number of requests scale with the number of campaigns, which is the direction
  that gets worse as the platform succeeds, and it gives up the single consistent snapshot that
  makes priority resolution on the device well-defined.
- **Serve per-user payloads from a content delivery network.** Rejected at this scale. A payload
  varies per channel, so caching it at the edge means one cache entry per device with a hit rate
  bounded by how often a device synchronizes twice inside the expiry — which the two-tier bundle
  cache achieves at the origin without the invalidation problem a network edge adds.
- **A cryptographic hash for the entity tag.** Rejected as unnecessary. The tag detects staleness
  between a server and a device that already trust each other over an authenticated channel, so
  collision resistance against an adversary buys nothing and costs throughput on the hottest path in
  the platform.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [x] Unit 1 — Content-derived entity tag over the elements that determine the rendered result.
- [x] Unit 2 — Conditional request with a per-channel tag cache serving the no-change path.
- [x] Unit 3 — Cursor-based delta with tombstones and a bounded change log, falling back to full.
      `internal/delivery/changelog` is the bounded log (`migrations/0013_delivery_change_log.sql`),
      retained seven days and pruned opportunistically on write rather than by a scheduler this phase
      doesn't have (`changelog.Retention`'s own comment gives the reasoning). `internal/delivery/cursor`
      is the opaque token: a change log sequence, the channel's segment membership bitmap hash
      (`internal/membership/reverse.Hash`, added for this and reused by Unit 4), and an issuance
      timestamp. `internal/delivery/deliver`'s `computeDelta` composes the three: a membership hash
      mismatch, a seq the log no longer covers, or an undecodable cursor all fall back to a full
      payload and a fresh cursor, never an error. The per-channel eligibility problem this box's prior
      note flagged — a channel entering or leaving a segment — is what the membership hash resolves:
      the hash mismatch catches any such change, so the change log itself needs to track message-level
      events alone, and the sole one that exists today is
      `internal/platform/connectserver/admin.go`'s `UpdateMessageState` (`CreateMessage` always
      creates in draft, never eligible, so it needs no entry). A message's delivery window opening
      needs no change log write either — the cursor's own issuance timestamp against the message's
      `window_start` catches it — and a window closing needs no tombstone at all, since
      `payload.Entry.ExpiresAt` already carries CW-0002's self-expiry contract. Delta mode is gated by
      `deliver.Config.DeltaModeEnabled`, off by default; no project entity exists yet to hold a
      genuine per-project switch, so this is a phase-one stand-in matching how CW-0007's
      `ProjectBudgetCap` already handles the same gap. Tested in
      `internal/delivery/changelog/changelog_integration_test.go` (monotonic seq, the `Since` query,
      pruning), `internal/delivery/cursor/cursor_test.go` (encode/decode, garbage never errors), and
      `internal/delivery/deliver/deliver_integration_test.go` (a recent cursor gets only what changed,
      a removed message gets a tombstone, a garbage or too-old cursor falls back to full, delta mode
      off never produces a cursor) — plus
      `internal/platform/connectserver/admin_integration_test.go`'s
      `TestUpdateMessageStateRPCRecordsTheChangeLog` proving the real `UpdateMessageState` call site
      writes it, not merely the isolated logic.
- [x] Unit 4 — Two-tier assembly cache: shared bundle plus per-channel overlay.
      `internal/delivery/payload/bundle.go` splits `Build`'s old single-pass assembly (`buildEntry`,
      the function CW-0008's own Progress notes cite — its logic now lives in `toBundleMessage` and
      `applyOverlay` below, though this pass does not update that other item's file) into
      `assembleShared` (eligibility and content, cached as a `bundle` in Redis under
      `delivery:bundle:<language>:<declaredMajor>:<membershipHash>` with no expiry, per this unit's
      own text: "invalidated by campaign edits, not by time") and `applyOverlay` (CW-0008's variant
      assignment and holdout, run fresh on every request regardless of cache hit or miss). The
      bundle's shared inputs are language (`SyncRequest.language`), the channel's declared schema
      major, and its segment membership bitmap hash (`internal/membership/reverse.Hash`, added for
      this and shared with Unit 3's cursor). Two inputs this unit's text also names — platform and
      application version — are not part of the key: neither exists as a typed, delivery-visible
      field anywhere in this schema today ("app_version" exists only as an audience-predicate
      attribute CW-0004's registry gates, with no production registry configuration in this
      repository to read a canonical value from; "platform" does not exist at all), and, more to the
      point, neither is an actual input to today's eligibility or content computation — `Build` never
      branches on either — so omitting them from the key costs nothing today. If either becomes a
      real content-selection input later, the key needs to grow to include it.

      Invalidation is keyed by segment, not by campaign id. A message-id-keyed secondary index (the
      option this unit's own instructions suggested) can only ever record bundles a message already
      appears in, so a campaign the kill switch has just activated — never in any bundle before that
      moment — would have no index entry to invalidate under, leaving every bundle already warm for
      its audience segment stuck serving "not yet eligible" forever, since a bundle carries no
      expiry. Indexing by segment (`delivery:bundle:segment-index:<segmentID>`, populated from the
      full `segmentIDs` list `assembleShared` queried against, not just the segments its resulting
      messages happen to carry) fixes this: a bundle is discoverable by every campaign that could
      ever become relevant to it, not only the ones it already contains. `InvalidateCampaign` reads
      but deliberately never deletes the index set itself, to avoid a race against a concurrent
      cache-miss recomputation's own write — see the function's own comment for the full reasoning.
      `internal/platform/connectserver/admin.go`'s `UpdateMessageState` calls it at the same trigger
      point Unit 3's change log uses.

      Tested in `internal/delivery/payload/bundle_integration_test.go`
      (`TestBuildAppliesVariantAssignmentFreshPerChannelFromASharedBundle`: two channels sharing one
      cached bundle still get their own CW-0008 assignment, not the first channel's replayed for the
      second; `TestInvalidateCampaignDropsOnlyBundlesContainingTheEditedMessage`: editing one
      campaign invalidates only the bundles reachable through its segment, leaving an unrelated
      segment's bundle untouched) and
      `internal/platform/connectserver/delivery_integration_test.go`'s
      `TestUpdateMessageStateRPCInvalidatesTheChannelsCachedBundle` for the real `UpdateMessageState`
      wiring end to end over HTTP. Every pre-existing `internal/delivery/payload` test continues to
      pass unchanged, since `Build`'s external behavior is identical — only its internals now cache
      the shared half.
- [x] Unit 5 — Server-dictated interval with jitter, and exponential backoff with full jitter.
      The interval and jitter are CW-0002 Unit 3's `internal/delivery/sync` package, reused here
      unchanged and returned on every Sync response, including the unchanged path. Backoff is device
      behavior, out of scope for this repository the same way CW-0002 Unit 5 is.
- [x] Unit 6 — Compression, the post-compression size ceiling, and the truncation metric.
      Compression is inherent to the Connect protocol already in use: a Connect handler supports gzip
      by default, negotiated from the request's headers, with no extra code. The size ceiling and the
      truncation metric are CW-0002 Unit 2's, now labeled by language so the metric names a cohort
      rather than only a count.

## References

- [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) — the payload
  contract this protocol transports, including its completeness and self-expiry properties.
- [CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md) — the
  membership index whose bitmap hash forms part of the bundle cache key.
- [`docs/requirements.md`](../../docs/requirements.md) — the delivery application programming
  interface (API) requirements this protocol satisfies, including the conditional request and the
  synchronization hint.
