**English** · [日本語](README-ja.md)

# citywalk roadmap

This directory holds the design record for citywalk's server backend. Each entry states one
decision — what we are building, why, and what we rejected — in the format an architecture decision
record (ADR) serves: a durable answer to "why is it built this way", readable years later by someone
who was not in the room.

Every entry is a **CW item**. **CW** stands for *citywalk Evolution*, and each item carries a
zero-padded four-digit identifier that never changes and is never reused.

## Browsing

[**The roadmap site**](https://0x0c.github.io/citywalk/) puts every item on one page:

- the status of each item, and the share of each topic already implemented;
- the checklist progress inside an item;
- a map of which items name each other under `Related`.

A build reads this directory, and every push republishes the page. The site never lags behind the
roadmap. `make site` builds the page locally, and `make serve` serves it at
`http://127.0.0.1:8000`.

## Layout

One directory per item, flat under `roadmaps/`:

```
roadmaps/
  CW-0001-in-app-message-platform-scope/
    CW-0001-in-app-message-platform-scope.md      ← English
    CW-0001-in-app-message-platform-scope-ja.md   ← Japanese
```

Both language files carry the same identifier and the same slug. The Japanese file is not a
mechanical translation: its `#` heading is written in Japanese, and its prose is rewritten to read
naturally rather than tracking the English sentence by sentence.

## Adding an item

1. **Allocate the next identifier** — the highest existing `CW-NNNN` plus one, across every
   directory under `roadmaps/`. Never reuse, skip, or guess a number.

   ```bash
   ls -d roadmaps/CW-*/ | sort | tail -1
   ```

2. **Create the directory and both language files** with `Status: Proposal`. A new item is always a
   proposal first.

3. **Write the prose under the writing norm.** Invoke the
   [`document-writing`](../.agent-workflows/document-writing/workflow.md) skill before drafting,
   together with [`english-document-writing`](../.agent-workflows/english-document-writing/workflow.md)
   for the English file and
   [`japanese-document-writing`](../.agent-workflows/japanese-document-writing/workflow.md) for the
   Japanese one. The norm shapes the draft; it is not a proofreading pass afterward.

4. **Run textlint on both files** and revise until every finding is gone. The runtime and the
   configuration live under
   [`.agent-workflows/document-writing/textlint/`](../.agent-workflows/document-writing/textlint/).

Identifiers are permanent. An item keeps its number when its status changes, when its
implementation ships, and when a later item supersedes it.

## Format

Each file opens with a language switcher line, then the `#` heading, then a metadata block, then
six sections in this order.

| Section | Contents |
|---|---|
| `Introduction` | What the item proposes, stated up front |
| `Motivation` | The problem, the background, and why the problem matters |
| `Detailed design` | The design, broken down mutually exclusively and collectively exhaustively |
| `Alternatives considered` | Each rejected option, with the reason for rejecting it |
| `Progress` | A checklist mirroring the `Detailed design` breakdown, one box per unit of work |
| `References` | Links and sources, each with a sentence saying what the reader gets from it |

The metadata block is delimited so that tooling can parse it:

```markdown
<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0001](CW-0001-<slug>.md) |
| Author | [@handle](https://github.com/handle) |
| Status | **Proposal** |
| Topic | Delivery model |
| Related | [CW-0002](../CW-0002-<slug>/CW-0002-<slug>.md) |
<!-- /CW-METADATA -->
```

`Related` and `Superseded by` are optional and reciprocal: when one item supersedes another, the
superseding item lists the older one under `Related`, and the older one names its successor under
`Superseded by`.

### Status

`Status` is the single source of truth for where an item stands. The directory path never depends
on it, so a promotion moves no files and breaks no links.

| Status | Meaning |
|---|---|
| `Proposal` | Under consideration, no code yet |
| `In progress` | Accepted and actively being built |
| `Implemented` | Shipped |
| `Proposal (deferred)` | Deliberately parked |

The code decides the status. An item authored with no implementation is a `Proposal`; the change
that ships its code sets `Status` to `Implemented`, or to `In progress` when it lands one slice,
ticks the matching `Progress` boxes, and records the pull request in the same change. `Proposal`
never stands on an item whose code has already shipped.

### Topic

`Topic` groups items for browsing. The topics in use today:

| Topic | Covers |
|---|---|
| Delivery model | How a message reaches a device and who decides what it sees |
| Targeting | Audience predicates, segments, and experiment assignment |
| Display governance | Frequency caps, priority, and conflict resolution |
| Measurement | Event collection, aggregation, and reporting |
| Platform | Runtime, storage, and cross-cutting infrastructure |

## Items

| ID | Item | Topic |
|---|---|---|
| [CW-0001](CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md) | In-app message platform: scope and decomposition | Platform |
| [CW-0002](CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) | Hybrid delivery: server-resolved audience, device-evaluated triggers | Delivery model |
| [CW-0003](CW-0003-message-definition-schema/CW-0003-message-definition-schema.md) | Message definition schema and its evolution | Delivery model |
| [CW-0004](CW-0004-audience-predicate-engine/CW-0004-audience-predicate-engine.md) | Audience predicate engine | Targeting |
| [CW-0005](CW-0005-segment-membership-index/CW-0005-segment-membership-index.md) | Segment membership index | Targeting |
| [CW-0006](CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) | Delivery payload synchronization | Delivery model |
| [CW-0007](CW-0007-display-governance/CW-0007-display-governance.md) | Display governance: caps, priority, and conflict resolution | Display governance |
| [CW-0008](CW-0008-deterministic-experiment-assignment/CW-0008-deterministic-experiment-assignment.md) | Deterministic experiment and holdout assignment | Targeting |
| [CW-0009](CW-0009-event-ingestion-analytics/CW-0009-event-ingestion-analytics.md) | Event ingestion and measurement pipeline | Measurement |
| [CW-0010](CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md) | Runtime and technology stack | Platform |

## Related documents

- [`docs/requirements.md`](../docs/requirements.md) — the requirement catalog these items design
  against, with an identifier per requirement.
- [`scripts/build_roadmap_site.py`](../scripts/build_roadmap_site.py) — the generator behind the
  roadmap site. Update the generator when the metadata format changes.
