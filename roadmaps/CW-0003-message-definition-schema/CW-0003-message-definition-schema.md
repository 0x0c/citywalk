**English** · [日本語](CW-0003-message-definition-schema-ja.md)

# CW-0003 — Message definition schema and its evolution

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0003](CW-0003-message-definition-schema.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **In progress** |
| Topic | Delivery model |
| Related | [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md), [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) |
<!-- /CW-METADATA -->

## Introduction

We propose the schema for a message definition, and the compatibility policy that lets it change
without stranding devices. A definition splits into a campaign part that no channel can see and a
content part that renders, the content is a tagged union over layout kinds rather than a free-form
document, every content object carries a schema version, and a software development kit (SDK) that
meets a version it does not know skips that message instead of guessing. Within a major version the
schema only ever gains optional fields.

The compatibility policy is the part that needs stating up front, because a mobile platform gives no
way to take a schema back. The oldest SDK version in citywalk's installed base will be two years old
and will still be fetching payloads, so any field the server emits today must remain intelligible to
a client built before the field existed.

## Motivation

A message definition looks like a rendering concern and is really a distributed-systems concern. The
server writes it, a device built at some unknown earlier date reads it, and there is no moment at
which both sides can be upgraded together. Every design choice in the schema is therefore a choice
about what happens when the two sides disagree about what a field means.

The tempting shortcut is a free-form document: store whatever the administrative interface produces,
send it through, and let the SDK render what it recognizes. The shortcut buys a fast first campaign
and then charges for it twice. The server can no longer validate anything, so a typo in a color or a
missing button label reaches devices and fails there, where it is invisible and unfixable within the
campaign's lifetime. And nothing records which shapes are legal, so the SDK's renderer becomes the
specification by accident — every field it happens to tolerate is now a compatibility obligation
nobody wrote down.

The opposite shortcut, a rigid per-layout table in the database, fails from the other side. A new
layout then needs a schema migration, a server release, and an SDK release before a campaign author
can use it, which puts the platform back on the release cycle that
[CW-0001](../CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md) exists
to escape. The schema below takes validation from the rigid design and extensibility from the
free-form one: the structure is typed and checked on the server, and the checking is driven by a
registry that a new layout extends without a migration.

## Detailed design

### Unit 1 — Entity split

A definition divides into five entities, along the line
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) draws between what the
server keeps and what the device receives.

- **Message** — the campaign: name, state, priority, delivery window, audience reference, governance
  policy, holdout fraction, and the conversion event. The audience reference never leaves the server.
- **Variant** — one renderable content object, with a weight for the experiment split and a language
  tag. A message with one variant and one language has exactly one.
- **Trigger** — a kind, an occurrence goal, and an optional predicate over event properties.
- **DisplayCondition** — a delay, a screen allowlist or denylist, and a connectivity requirement.
- **ControlPolicy** — the per-message caps, the minimum interval between impressions, and whether the
  message is exempt from the project-wide cap.

Splitting Variant out of Message is what makes an A/B test and a translation the same mechanism
rather than two, since both are "the same campaign, different content". The delivery service selects
one variant per device by language first and then by experiment assignment.

### Unit 2 — Content as a tagged union

Content is a tagged union discriminated on `layout`, with one member per display form: a centered
dialog, an edge banner, a full screen, arbitrary HTML, and a multi-step sequence whose steps are
themselves dialog or full-screen members. Each member declares its own fields, and the shared fields
— heading, body, media reference, buttons, colors, corner radius, automatic-close delay — are
declared once and reused rather than repeated per member.

A button is a label plus an ordered list of actions, where an action is itself a tagged union: close,
open an external link, navigate to an in-application destination, emit a custom event, or set an
attribute. Modeling a button's behavior as a list rather than a single action is what lets one press
both record a custom event and navigate, which is the common case and would otherwise need a second
mechanism.

### Unit 3 — Storage representation

The campaign entities are relational columns: message state, priority, window, and policy are
queried, filtered, and joined by the administrative interface and by the delivery service, and each
wants an index. Content is stored as one JSON document per variant, because the delivery service
never queries inside it — content is opaque data that gets copied into a payload — and a relational
decomposition of a tagged union would produce a table per layout and a join per read for no gain.

The split is a rule rather than a case-by-case judgment: a field goes in a column when something
filters or sorts on it, and in the JSON document when it is only ever read whole. Applying the rule
keeps the indexes meaningful and keeps a new layout from touching the database schema.

### Unit 4 — Versioning and compatibility

Every content object carries a `schema_version` with a major and a minor part. Within a major
version, changes are additive only: a new optional field, a new action kind, a new layout member. An
SDK that meets an unknown optional field ignores the field; an SDK that meets an unknown `layout` or
an unknown action kind skips the whole message, since rendering half of an unknown layout is worse
than rendering nothing.

A major version increment is a breaking change and is delivered by parallel emission rather than by
migration: the server emits both versions for the overlap period, each device receives the highest
version its SDK declares support for, and the old version is withdrawn once its share of active
devices falls below a stated threshold. The device declares that support at registration, so the
delivery service knows what to emit without asking.

Skipping a message must be observable, or a rollout that strands a cohort looks like a campaign
nobody qualified for. The SDK therefore reports a skip as a measurement event carrying the message
identifier and the unsupported version.

### Unit 5 — Save-time validation

The definition service validates on save and rejects rather than warns. Validation covers the
structural shape against the schema for the declared version, referential integrity (the referenced
segment, conversion event, and media exist), temporal sanity (the window's start precedes its end,
and the end is in the future), the variant weights summing to the expected total, and the security
constraints on content — a link scheme outside the allowlist, or HTML content carrying an
executable scheme, is a rejection.

Validating at save time rather than at delivery time is the whole point: a rejection at save reaches
the person who can fix it, while a rejection at delivery reaches a device and nobody at all.

## Alternatives considered

- **A free-form content document with no schema.** Rejected. The server can validate nothing, so
  errors surface on devices where they are invisible and unfixable mid-campaign, and the SDK's
  renderer becomes an undocumented specification by accident.
- **A relational table per layout.** Rejected. Every new layout becomes a database migration and a
  server release, which reinstates the release-cycle coupling the platform exists to remove, and the
  read path gains a join that buys nothing since content is never queried by its fields.
- **Server-rendered HTML for every message.** Rejected. One rendering path is genuinely simpler, but
  it gives up native presentation on both platforms, makes the payload much larger, and puts a
  content-injection surface in front of every message rather than only the campaigns that opt into
  HTML.
- **Negotiating capabilities per field instead of per schema version.** Rejected as over-general. A
  device advertising a set of supported fields is more precise than a version number, but it moves
  the compatibility matrix from one dimension to many and makes the emitted payload depend on a
  combination the server cannot enumerate for testing.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [x] Unit 1 — The five entities and the message-to-variant split.
- [x] Unit 2 — Content as a tagged union over layouts, with actions as a nested union.
- [x] Unit 3 — Relational columns for queried fields, one JSON document per variant for content.
- [x] Unit 4 — Schema versioning, additive-only minor changes, parallel emission across a major.
      Versioning, the compatibility check, and the mechanism parallel emission needs are now wired
      end to end, and save-time validation now admits more than one major. `validateSchemaVersion`
      checks a variant's declared major against `model.SupportedMajors`
      (`internal/definition/model/schema_version.go`), a small explicit set holding
      `model.CurrentMajor` alone until a migration begins, at which point it names the next major too.
      That widening is what lets a message persist both majors' content side by side, so
      `payload.Build`'s exclusion logic has two majors to choose between instead of one. A device
      declares its supported major at registration (CW-0010 Unit 9's Register), and `payload.Build`
      reads it back and excludes any variant `SchemaVersion.SupportsMajor` rejects before selection
      ever reaches language or experiment assignment — a device gets no entry for a message with no
      compatible variant, the same as a channel that never qualified. Widening `SupportedMajors`
      leaves additive-only-within-a-minor untouched: an unknown `layout` or action kind is still a
      decode rejection (`model.UnmarshalContent`, `model.UnmarshalAction`), whatever majors save-time
      validation admits.
- [ ] Unit 5 — Save-time validation covering shape, references, time, weights, and content security.
      Shape, temporal sanity, weights, content security, and audience referential integrity (a
      non-empty `audience_ref` must name a real row in `segments`) are implemented and tested.
      Conversion event and media referential integrity are not — the conversion event catalog has no
      owner yet, and media existence depends on the object storage CW-0010 Unit 7 defers to a later
      phase, so neither exists as a queryable store yet.

## References

- [`docs/requirements.md`](../../docs/requirements.md) — the message definition and validation
  requirements this schema satisfies.
- [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) — the boundary that
  decides which entities reach the device.
- [CW-0006](../CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md) — the synchronization
  protocol that carries versioned content to devices.
