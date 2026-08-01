# Implementing a roadmap item

This workflow takes one citywalk roadmap item from `Proposal` (or `In progress`) to shipped,
gate-verified Go code. citywalk carries no application code yet — every `CW-NNNN` item is a design
record, and this workflow is what turns one into code: built to the stack
[CW-0010](../../roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md)
already settled, and to the design the item's own `Detailed design` section already settled. This
workflow does not design; the roadmap item is the design. It authors, verifies, and ships.

## Guardrails

Every step below runs inside these guardrails. None of them bend for convenience, for time
pressure, or because a shortcut would be easier to code. A step that runs into one of them stops
and asks the human instead of working around it.

- **The roadmap item is the spec of record.** Implement what its `Detailed design` says, the whole
  unit at a time, not an approximation of it. If implementation surfaces a place where the design is
  wrong, incomplete, or conflicts with code that already exists, stop and say so. Propose a revision
  to the roadmap prose as its own change — never quietly implement a different design and let the
  document drift out of sync with the code.
- **A human approves the plan before any code is written.** This holds for a one-line change as much
  as for a whole service. Step 5 is not a formality to skip past.
- **One item, one branch, one focused change.** Touch only what the item's `Detailed design` needs.
  If the design forces a change outside that scope — a shared library, another service's interface —
  flag it up front rather than folding it in silently.
- **Stack discipline.** Build against the choices CW-0010 already settled: Go, Protocol Buffers
  served over Connect, `pgx`/`sqlc` against PostgreSQL, `go-redis`, `cel-go`, `river`, OpenTelemetry,
  and the rest of CW-0010's library baseline. A different language, framework, or store is a change
  to CW-0010, never an implementation detail decided inside another item.
- **Bilingual roadmap records move together.** A `Status` or `Progress` edit lands in the English
  file and the Japanese file in the same commit. Never one without the other.
- **Determinism over convenience.** No test depends on a wall-clock sleep, on network reachability
  it does not declare, or on an ordering the code does not guarantee. A flaky test is a bug in the
  test, fixed the same as any other bug — never quarantined or retried into passing.
- **The mechanical gate decides pass or fail, not a self-assessment.** "Looks fine" is never a
  substitute for a clean `gofmt`, `go vet`, build, and test run. If the gate fails for a reason that
  looks like a design problem, that goes back to grounding and planning, not around the check.
  Nothing that fails the gate gets committed.
- **Self-review is mandatory, not optional under time pressure.** Every change gets a full review
  pass — mechanical and semantic — before it is offered as done. Every finding gets fixed, or is
  named to the human as a deliberate trade-off and signed off — never silently dropped.
- **No secret material in the repository.** Anything that varies by environment — credentials,
  connection strings, tokens — lives in configuration kept out of version control, never
  hard-coded or committed.
- **`Status` only moves forward, and only as far as the code justifies.** `Implemented` is set only
  once every unit in the item's `Detailed design` has shipped and its `Progress` box is checked. A
  partial slice sets `In progress` and leaves the remaining boxes unchecked — never `Implemented` on
  a promise that the rest follows later.

## Step 1 — Resolve the item

Accept the item by full identifier (`CW-0004`), bare number (`4` or `0004`), or a slug fragment.
Locate its permanent directory:

```bash
ls -d roadmaps/CW-*<id-or-slug>*/
```

Read both language files in full. The English file is authoritative; the Japanese file is
supporting context. Before doing anything else, explain the item to the human in plain language: its
ID and title, its `Status` and `Topic`, a summary of `Introduction` and `Motivation` in your own
words, and how much of `Progress` is already checked.

Branch on `Status`:

- **`Proposal`.** The normal case. Implementing it is what accepts the proposal; say so, and note
  that the `Status` flip itself waits for step 9, once the shipped units justify it.
- **`In progress`.** Read which `Progress` boxes are already checked and which units remain. If the
  human has not said which remaining unit this pass covers, ask.
- **`Implemented`.** The item has already shipped. Stop and confirm what the human actually wants —
  a fix, an extension — before proceeding. A genuine extension is very likely its own new item, since
  an identifier never changes what it originally recorded.
- **`Proposal (deferred)`.** Deliberately parked. Stop and confirm the human wants to un-defer it
  before building anything.

## Step 2 — Check for a collision

citywalk has no dedicated tracking-issue convention yet, so this is a best-effort check rather than
a claim-and-lock: look for an open pull request or branch already carrying this item's identifier
(`git branch -r`, and a search of open pull requests for `[CW-NNNN]` in the title) before starting.
If one exists and is not the human's own prior work, stop and surface it — do not duplicate work in
flight.

## Step 3 — Ground in the spec and the code

- Read the item's `Detailed design` and `Alternatives considered` in full. The alternatives record
  paths already rejected and why; do not re-propose one.
- Read every item this one lists under `Related`, plus the sections of
  [`docs/requirements.md`](../../docs/requirements.md) whose `FR-`/`NFR-` identifiers the design
  maps to. Confirm the design actually covers the requirements it claims to.
- Read [CW-0001](../../roadmaps/CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md)
  and [CW-0010](../../roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md)
  regardless of which item is in hand — they set the service boundary and the stack every
  implementation lands inside.
- Survey what code already exists (`Glob`/`Grep` across the repository). Do not rebuild something
  already shipped, and match the package layout, naming, and error handling already established. If
  no Go code exists yet, this pass is the first one, and the plan in step 5 has to include the
  scaffold that later passes will build on: the module, the package layout that keeps CW-0001's five
  services separated in code even while they run as one process (per CW-0010 Unit 11), the migration
  directory, the `make check` gate, and a CI workflow that runs the same gate.
- If a `Related` item this design depends on is itself still `Proposal`, that is a blocker. Surface
  it and ask whether to implement the prerequisite first or to find a smaller slice that does not
  need it.

## Step 4 — Set up a focused workspace

Branch off the latest default branch, one item per branch:

```bash
git fetch origin && git switch -c claude/cw-NNNN-<slug> origin/<default-branch>
```

If already on a branch dedicated to this item, stay there.

## Step 5 — Plan, then stop for approval

State the plan before writing implementation code, naming:

- Which unit or units of the item's `Detailed design` this pass covers — all of them, or a named
  subset for a large item — and the `Progress`/`Status` state that results once this pass ships.
- The files and packages to add or change, including any scaffold this pass establishes.
- The `FR-`/`NFR-` requirement identifiers this pass satisfies, and the test that will demonstrate
  each one.
- The tests to add, including whether any need Postgres or Redis and how they are tagged so they are
  skippable without those services running.
- Whether anything under `docs/` needs a bilingual update beyond the roadmap item itself.
- Anywhere the plan comes close to a guardrail above, and why it doesn't actually cross it. A plan
  that does cross one is not a plan to implement — it is a question for the human.

Wait for explicit approval. Do not write implementation code before it.

## Step 6 — Implement

Build to the approved plan, one unit at a time.

- Match the surrounding code once it exists. Where this pass is establishing the pattern — the first
  code in a package, or the first Go code in the repository — follow idiomatic Go and the library
  choices CW-0010 names, and keep comments to the *why*, at the same restrained density this
  repository already holds its prose to: no narration of *what* the code already says.
- Handle every error explicitly. No swallowed error and no fallback that quietly hides a failure the
  requirements need observable — NFR-OBS-01 through NFR-OBS-04 exist because a silent failure here
  is a campaign that under-delivers with nothing in a dashboard to explain why.
- Write the test that proves the requirement, not a test that merely calls the code. Cover new logic
  with fast unit tests; where a test genuinely needs Postgres or Redis, put it behind a build tag and
  document how to run it.
- Update `docs/` in both languages in the same change, if this pass changes behavior those documents
  describe. Draft any prose edit under the
  [`document-writing`](../../.claude/skills/document-writing/SKILL.md) skill (and its language
  layer) before revising.

## Step 7 — Self-review: the mechanical gate

Run the following, and keep fixing and rerunning until every one is clean. This is a gate, not a
suggestion — nothing that fails it gets committed:

```bash
gofmt -l .              # must print nothing
goimports -l .          # must print nothing, if goimports is set up
go vet ./...
golangci-lint run        # if .golangci.yml exists; this pass adds one if it's the first Go code
go build ./...
go test ./...            # plus the tagged integration suite, where the services it needs are reachable
```

If a check fails for a reason that looks like the design itself is wrong, that is a return to step 3
or step 5 — re-ground or re-plan — not a loosened check or a suppressed failure.

## Step 8 — Self-review: the semantic pass

Independent of the mechanical gate, reread the entire diff fresh — through a separate subagent or a
fresh context where the host supports one, so the review is not anchored on the reasoning that wrote
the code. Check at minimum:

- Does the diff implement the whole unit the `Detailed design` describes, not a partial
  approximation of it?
- Silent failures: a swallowed error, or a fallback that hides a problem the requirements need
  observable.
- Security and privacy: no directly identifying personal information beyond what NFR-SEC-05 allows,
  Transport Layer Security assumptions intact (NFR-SEC-01), nothing confidential reaching a payload
  the device can inspect (NFR-SEC-03), no secret committed.
- Determinism: nothing depends on wall-clock timing, unstated ordering, or a reachable-but-undeclared
  external service.
- Simplicity: no abstraction the current unit does not call for, no generalization built ahead of a
  unit that has not shipped yet.
- Test coverage: does a test actually assert the behavior the requirement demands, not just execute
  the code path without checking its result?

List every finding. Fix each one, or bring it to the human as a deliberate trade-off and get sign-off
before treating it as resolved. Then repeat step 7 and step 8 together until one full pass of both
turns up nothing. Never present the change as finished while a finding from either pass is still
open.

## Step 9 — Update the roadmap record

In both language files: check the `Progress` boxes this pass completed, and set `Status` to
`Implemented` only once every box is checked — otherwise `In progress`. This edit lands in the same
change as the code that earns it, never as a follow-up. The item's directory never moves and its
identifier never changes, regardless of `Status` — see
[`roadmaps/README.md`](../../roadmaps/README.md).

## Step 10 — Commit and open the pull request

Commit with scoped, imperative messages. Open the pull request titled `[CW-NNNN] <summary>`. If a
pull request template exists at that point, follow it; otherwise write a body that states which
units shipped, which `FR-`/`NFR-` identifiers this pass satisfies, and the gate's output. Open it as
a draft where the host supports drafts, so a human decides when it is ready — this workflow does not
mark its own pull request ready. Keeping the pull request green afterward (CI fixes, review replies)
follows whatever standing convention the host already applies; this workflow does not define a
second one.

## When to stop and ask

- The item's `Status` is `Implemented` or `Proposal (deferred)` (step 1).
- Another open branch or pull request already carries this item's identifier (step 2).
- A `Related` item this design depends on is still `Proposal` (step 3).
- The design needs a change outside the item's own scope, or a change to the CW-0010 stack (step 3,
  guardrails).
- The plan is not yet approved (step 5) — no code before this.
- The mechanical gate keeps failing for what looks like a design reason, not a typo (step 7).
- A semantic-review finding is not a clear fix and needs a trade-off the human should decide (step
  8).
- Anything that would put a secret in the repository, or move `Status` further than the shipped code
  justifies (guardrails).

## References

- [`roadmaps/README.md`](../../roadmaps/README.md) — the roadmap item format, the `Status` field,
  and the rule that an item's directory and identifier never change.
- [`docs/requirements.md`](../../docs/requirements.md) — the requirement catalog every `Detailed
  design` maps onto.
- [CW-0001](../../roadmaps/CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md)
  — the five-service decomposition every implementation keeps as a boundary in the code.
- [CW-0010](../../roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md) —
  the runtime, the stores, and the library baseline every implementation builds against.
- [`document-writing`](../document-writing/workflow.md) — the norm for any bilingual prose this
  workflow's step 6 or step 9 touches.
