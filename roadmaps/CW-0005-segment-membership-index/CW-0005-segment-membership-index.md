**English** · [日本語](CW-0005-segment-membership-index-ja.md)

# CW-0005 — Segment membership index

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0005](CW-0005-segment-membership-index.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **In progress** |
| Topic | Targeting |
| Related | [CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md), [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) |
<!-- /CW-METADATA -->

## Introduction

We propose storing segment membership as two indexes over one relation, each shaped for the query
that reads it. The forward index maps a segment to the set of channels in it, stored as a compressed
bitmap; the authoring interface reads it to count a segment and to intersect two of them. The reverse
index maps a channel to the set of segments it belongs to, stored as a small bitmap per channel; the
delivery service reads it on every payload assembly. A single batch computation writes both, an
incremental path keeps both current between batches, and a generation number makes the swap between
two computations atomic.

Two indexes over one fact is a deliberate duplication, and the reason is that the two readers have
opposite access patterns. Counting a segment wants every channel in one segment; assembling a payload
wants every segment for one channel. No single layout answers both in the time each has.

## Motivation

The delivery service must answer "which segments does this channel belong to?" inside a payload
assembly that has a 200 millisecond budget at the 99th percentile, several times per second per
server, for ten million distinct channels. That is the query that decides the design, and the two
obvious storage choices fail it in different ways.

Evaluating the segment predicates at request time is the simplest thing that could work and is what a
first implementation reaches for. It fails on cost: a project with fifty segments evaluates fifty
predicates per request, several of which read event aggregates, and the work is repeated identically
for every request from every device even though the answer changes at most hourly. Precomputation is
not an optimization here; it is the difference between a workload proportional to requests and one
proportional to changes.

Storing membership as rows in a relational table — one row per segment and channel pair — is the
second reach, and it fails less obviously. The storage is fine: ten million channels across twenty
segments each is two hundred million rows, which any relational database holds. The reverse lookup is
also fine, being an index scan over a handful of rows. What fails is everything the authoring
interface does. Counting a segment scans its rows; asking how much two segments overlap joins two
large row sets; asking how many channels a proposed predicate would add scans again. Those queries
run while a campaign author waits, and at that row count they take seconds rather than milliseconds.

A compressed bitmap answers exactly the queries rows are bad at. Counting is a population count over
words, intersection is a word-wise `AND`, and a segment of one million channels occupies a few
hundred kilobytes rather than the tens of megabytes an uncompressed bitset of ten million bits would
need per segment. The design below therefore keeps rows out of the read path entirely and stores the
same membership twice, once in each direction.

## Detailed design

### Unit 1 — Channel ordinals

A bitmap indexes by integer, so each channel is assigned a dense ordinal on first registration,
held in a table alongside the channel's opaque identifier. Ordinals are never reused, and a deleted
channel's ordinal is retired, which keeps a stale bitmap from resurrecting a deleted channel under a
recycled number.

Density is what makes the compression work: ordinals allocated in registration order cluster the
channels of any recently defined segment into adjacent runs, which is the case the bitmap
representation compresses best.

### Unit 2 — Forward index

Each segment stores a Roaring bitmap of the ordinals in it. Roaring partitions the value space into
blocks and picks a representation per block — an array for a sparse block, a bitset for a dense one,
a run length encoding for a contiguous one — which is what keeps both a segment of a thousand
channels and a segment of eight million compact under the same structure.

The forward index serves the authoring interface: cardinality is the bitmap's population count,
overlap between two segments is the cardinality of their intersection, and the reach of a proposed
combination is the cardinality of the corresponding boolean expression over bitmaps. Each is a
word-wise operation over a few hundred kilobytes, which is fast enough to answer while an author
types.

### Unit 3 — Reverse index

Each channel stores a bitmap of the segment identifiers it belongs to. Segment identifiers are also
dense ordinals, and a project holds thousands of segments at most, so a channel's membership is a
bitmap of a few thousand bits — under a kilobyte uncompressed, and typically under a hundred bytes
compressed, since a channel belongs to few segments.

The delivery service reads exactly one of these per payload assembly, keyed by channel, from the
in-memory store. One key lookup returning under a hundred bytes is what makes the audience half of
payload assembly effectively free, which is the property
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) depends on.

### Unit 4 — Batch recomputation and the generation swap

A segment marked for periodic refresh is recomputed by the set-wise evaluator from
[CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md), which compiles
the segment's predicate to SQL and produces the full membership. The result is written under a new
generation number, and the generation pointer is advanced only once every affected index is written.

The generation indirection exists to prevent a torn read. Without it, a reader arriving mid-write
sees a segment that is half old and half new — which, for a campaign whose audience is an
intersection of two segments, produces an audience that never existed under either computation. With
it, a reader sees one complete generation or the other. Old generations are retained briefly and then
reclaimed, which also makes a bad recomputation revertible by moving the pointer back.

### Unit 5 — Incremental maintenance

Between batches, an attribute change must reach the index without recomputing everything. The
definition service maintains a dependency map from each attribute in the registry to the segments
whose predicates reference it, built by walking the stored predicate trees. When a channel's
attribute changes, the incremental path re-evaluates only the segments that depend on that attribute,
for that one channel, and flips the corresponding bits in both indexes.

The dependency map is what bounds the work: a change to a rarely referenced attribute costs one
predicate evaluation, not fifty. A segment whose predicate reads an event aggregate is excluded from
the incremental path and marked batch-only, since its membership can change without any attribute
changing — the passage of time alone moves a windowed count — and pretending otherwise would leave
the index silently stale.

### Unit 6 — Reconciliation

Incremental maintenance is an optimization over the batch, and the batch is the definition of
correctness. A full recomputation therefore runs on a schedule regardless of incremental activity,
and compares its result against the live index before swapping, reporting the count of channels that
disagreed.

That count is the health metric for the whole subsystem. A nonzero and growing disagreement means the
dependency map has a gap or an event was dropped, and both are failures that are invisible in every
other signal — a stale membership serves a payload that looks entirely normal.

## Alternatives considered

- **Evaluate segment predicates at request time.** Rejected on cost. The work scales with requests
  rather than with changes, so a project's fiftieth segment slows every device's synchronization,
  and the recomputed answer is identical to the previous one almost every time.
- **Store membership as relational rows.** Rejected on the authoring path. Rows serve the reverse
  lookup adequately but turn counting and set algebra into scans and joins over hundreds of millions
  of rows, which a campaign author waits seconds for. Rows remain the durable record behind the
  index; the objection is to serving reads from them.
- **A search engine such as Elasticsearch.** Rejected on operational cost. It would serve both query
  shapes and add faceting the authoring interface has no requirement for, at the price of a second
  stateful system to run, size, and keep consistent with the relational store — for a workload that
  two bitmaps and an in-memory store already cover.
- **A Bloom filter per segment.** Rejected on semantics. A Bloom filter answers membership compactly
  but admits false positives, which here means delivering a campaign to a channel outside its
  audience and reporting it as a legitimate impression. It also cannot count or intersect, which is
  half of what the forward index exists for.
- **A materialized view maintained by the database.** Rejected on control. It removes the incremental
  path from the platform's hands and its refresh cost is opaque, so the generation swap and the
  reconciliation metric — the two properties this design leans on hardest — would both have to be
  rebuilt on top of it anyway.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [x] Unit 1 — Dense channel ordinals, allocated on registration and retired on deletion.
- [x] Unit 2 — Forward index as a Roaring bitmap per segment, with cardinality and set algebra.
- [x] Unit 3 — Reverse index as a per-channel segment bitmap in the in-memory store.
- [x] Unit 4 — Batch recomputation with generation numbering and an atomic pointer swap.
- [x] Unit 5 — Incremental maintenance driven by an attribute-to-segment dependency map.
- [ ] Unit 6 — Scheduled reconciliation reporting the disagreement count as a health metric.
      The comparison and disagreement-count reporting run as part of every recomputation and are
      tested; nothing runs it on a schedule yet, since the job queue that would trigger it
      (CW-0010 Unit 8) doesn't exist.

## References

- [Roaring bitmaps](https://roaringbitmap.org/) — the compressed bitmap format both indexes use, and
  the source of the per-block representation choice described in Unit 2.
- [CW-0004](../CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md) — the
  predicate engine whose set-wise evaluator produces what this index stores.
- [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) — the delivery model
  whose payload assembly reads the reverse index.
