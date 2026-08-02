**English** · [日本語](CW-0004-audience-predicate-engine-ja.md)

# CW-0004 — Audience predicate engine

<!-- CW-METADATA -->
| Field | Value |
|---|---|
| Proposal | [CW-0004](CW-0004-audience-predicate-engine.md) |
| Author | [@0x0c](https://github.com/0x0c) |
| Status | **Implemented** |
| Topic | Targeting |
| Related | [CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md), [CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md) |
<!-- /CW-METADATA -->

## Introduction

We propose evaluating audience conditions with the Common Expression Language (CEL) — a small,
non-Turing-complete expression language with a Go implementation — over a typed attribute registry.
A predicate is authored as an expression, type-checked against the registry when it is saved, and
stored as a compiled abstract syntax tree rather than as a source string. One stored tree drives two
evaluators: a row-wise one that answers "does this channel qualify?" during payload assembly, and a
set-wise one that compiles the same tree to SQL to compute a segment's membership in bulk. A
conformance suite runs both over the same fixtures and fails when they disagree.

The two evaluators are the reason this needs a design item rather than a library choice. Audience
questions arrive in two shapes that differ by six orders of magnitude in cardinality — one channel
now, or ten million channels overnight — and an engine that serves only one of them pushes the other
into a second, hand-written implementation that drifts.

## Motivation

The audience predicate is the one place where a campaign author's input becomes something the server
executes, which makes its design a safety question before it is an expressiveness question. A
predicate is written by a person in an administrative interface, saved, and then run against every
channel in the project; if that input can express an unbounded loop, the evaluation never finishes,
and if it can express a data access the author should not have, the interface has become a data
exfiltration tool.

Two obvious answers fail on exactly those points. Letting authors write SQL is maximally expressive
and hands them the whole database, including the tables the platform does not own; no amount of
statement filtering makes that safe, because the filter has to be right every time and the attacker
only has to be right once. Letting authors write a general-purpose scripting language has the same
problem plus a termination problem: a predicate with a loop in it can run forever, and a payload
assembly that runs forever is an outage.

The third answer, a hand-rolled predicate interpreter, is safe and is where this design would land
by default. It fails on cost rather than on correctness. A usable predicate language needs a parser,
a type checker, comparison and set and string operators, semantic version ordering, relative time,
null handling, and error messages good enough for a non-engineer — and then it needs all of that
again in the SQL compiler. CEL supplies the front half as a specified, tested language whose
evaluation is guaranteed to terminate, which leaves this item to design what is actually specific to
citywalk: the attribute registry, the SQL compilation, and the event aggregates.

## Detailed design

### Unit 1 — The attribute registry

Every name a predicate may reference is declared in a registry: its identifier, its type (string,
number, boolean, timestamp, semantic version, or a set of strings), its source (a channel field, a
user attribute, a tag, or an event aggregate), and whether it is confidential. The registry is data,
so adding an attribute is a configuration change rather than a release.

The registry does three jobs at once. It types the predicate, so `app_version >= "2.10.0"` compares
as a semantic version and not as a string, where the string comparison would rank `2.9.0` above
`2.10.0`. It bounds the reachable data, since a name absent from the registry fails to compile and
no predicate can reach a table the registry does not describe. And it carries the confidentiality
flag that
[CW-0002](../CW-0002-hybrid-delivery-model/CW-0002-hybrid-delivery-model.md) needs: an attribute
marked confidential may appear in an audience predicate, which the server evaluates, and never in a
trigger predicate, which reaches the device.

### Unit 2 — Storage as a tree, not a string

A saved predicate is stored as its abstract syntax tree, serialized as JSON, alongside the original
source text for display. The tree is what executes; the source is documentation.

Storing the tree rather than the string is what makes the predicate stable under language changes:
an expression parsed today keeps its meaning when the parser is upgraded, because the parse already
happened. It also removes a class of failure at delivery time, since a payload assembly can no longer
encounter a syntax error — an unparseable predicate was rejected at save.

### Unit 3 — Row-wise evaluation

During payload assembly the server evaluates each candidate message's predicate against one channel's
attributes. The tree is compiled to CEL's executable program once and cached by the tree's content
hash, so the compilation cost is paid once per predicate rather than once per request, and a
predicate shared by several messages compiles once.

Evaluation is bounded on both time and data. CEL has no unbounded loop, so a program terminates in
time proportional to its own size; the attribute values come from a single map the caller assembles
before evaluation, so evaluation performs no input or output and cannot become slow because a
database is slow.

### Unit 4 — Set-wise evaluation

The same tree compiles to a SQL `WHERE` clause for batch computation, where a segment's membership is
recomputed over the whole channel table. Compiling the tree the campaign author actually wrote — as
opposed to maintaining a parallel SQL representation — is what keeps the two paths honest: there is
one predicate, and two backends for it.

Two backends over one language is nonetheless a place where a subtle divergence hides, so the item
includes a conformance suite as a deliverable rather than as a test detail. The suite evaluates every
predicate in a corpus against a fixture population through both backends and fails on any
disagreement, which is what catches the cases where the two differ by default: null and missing-value
handling, string collation, and timestamp boundary inclusion.

A construct that cannot be compiled to SQL is rejected at save time with a message naming the
construct, rather than being accepted and then failing during the nightly batch.

### Unit 5 — Event aggregates

A condition such as "opened the route screen at least three times in the last seven days" cannot scan
raw events at delivery time; at ten million channels the scan alone would exceed the latency budget.
The engine reads pre-aggregated counters instead: an ingestion-time rollup maintains a count per
channel, per event name, and per day, and a windowed condition sums the buckets covering its window.

Daily buckets fix the granularity of a relative-time condition at one day, which is a deliberate
trade rather than an oversight. Finer buckets multiply the rollup's storage by the ratio, and an
audience condition — as distinct from a trigger, which the device evaluates with exact local state —
has no use for hour-level precision. The registry records each aggregate's granularity so that the
type checker can reject a condition asking for precision the rollup does not carry.

### Unit 6 — Reach estimation

The administrative interface reports how many channels a predicate matches. An exact count runs the
set-wise evaluation, which is too slow for the interactive loop a campaign author works in, so the
interface estimates by evaluating against a uniform sample of channels and scaling, and returns the
estimate together with its confidence interval and the sample size.

Returning the interval rather than a bare number is the requirement, not a nicety: an author reading
"about 40,000" makes a different decision than one reading "40,000, give or take 12,000", and the
second is the honest answer at small sample sizes.

## Alternatives considered

- **Let authors write SQL.** Rejected on security. A predicate that reaches the database reaches
  every table in it, and statement filtering has to be correct on every input while an attacker needs
  one gap. The expressiveness is real, but no audience condition citywalk needs requires it.
- **A general-purpose embedded scripting language.** Rejected on termination and on sandboxing. A
  predicate containing a loop can run forever, and a payload assembly that never returns is an
  outage; sandboxing a full language is also a much larger surface to get right than adopting one
  that has no dangerous constructs to begin with.
- **A hand-rolled predicate interpreter.** Rejected on cost, not on safety — it is the safe design,
  and it is what CEL is a pre-built instance of. Writing it means writing a parser, a type checker,
  an operator set, and non-engineer-legible errors, then maintaining that language as requirements
  grow. The specific work would be identical to CEL's and would start untested.
- **JSONLogic or a similar JSON-encoded rule format.** Rejected on authoring and on typing. A rule
  expressed as nested JSON is hard for a person to read in a diff, and the format carries no type
  system, so the semantic-version comparison that motivates the registry would have to be bolted on
  as a custom operator with no checking behind it.
- **`expr-lang/expr` instead of CEL.** A close call, and worth revisiting if evaluation cost ever
  dominates: it is faster and its syntax is friendlier. CEL wins here on the specification and on the
  type checker, which is the piece Unit 1 leans on hardest, and on having a standard the SQL
  compilation can be validated against rather than a single implementation's behavior.

## Progress

> Keep this section current as work proceeds. Each box mirrors one unit in *Detailed design*.

- [x] Unit 1 — The typed attribute registry, including the confidentiality flag.
- [x] Unit 2 — Predicate storage as a serialized tree, with the source kept for display.
- [x] Unit 3 — Row-wise evaluation with a compilation cache keyed by tree hash.
- [x] Unit 4 — Tree-to-SQL compilation and the two-backend conformance suite.
- [x] Unit 5 — Event aggregate rollups and windowed conditions over daily buckets.
      Rollup maintenance (CW-0009's `targeting_rollup`, now that CW-0009 exists) and the windowed-sum
      evaluation are both implemented and tested against real Postgres, on both backends: `sqlcompile`
      compiles an event-aggregate attribute into a correlated subquery against `targeting_rollup`
      (`internal/audience/sqlcompile`), and `reach` merges the same sums into the row-wise evaluator's
      attribute map (`internal/audience/reach`) — verified end to end from real submitted events
      through CW-0009's consumer to a `batch_only` segment's membership. The type-checker rule now
      exists too. `predicate.validateAggregateGranularity`
      (`internal/audience/predicate/predicate.go`) walks a condition's tree. For every event-aggregate
      attribute it finds, it calls `registry.GranularityCarries`
      (`internal/audience/registry/registry.go`), which compares the attribute's registered
      granularity against the day-level precision every window requires (`AggregateWindowDays` is
      always a whole number of days). A coarser granularity fails that comparison, and the check turns
      down the condition. `AggregateGranularity` still names only `"day"` in production, so the rule
      leaves every real `Definition` alone today. `registry_test.go` and `predicate_test.go` exercise
      the comparison, and the end-to-end refusal, against synthetic, test-only "hour"/"week"
      granularities — proof the comparison already works the day someone registers a second, finer or
      coarser granularity for real.
- [x] Unit 6 — Sampled reach estimation reporting a confidence interval.

## References

- [Common Expression Language](https://github.com/google/cel-spec) — the specification for the
  expression language this engine evaluates, including its termination guarantee.
- [`cel-go`](https://github.com/google/cel-go) — the Go implementation this item builds the row-wise
  evaluator on.
- [CW-0005](../CW-0005-segment-membership-index/CW-0005-segment-membership-index.md) — the index
  that stores what set-wise evaluation computes.
- [`docs/requirements.md`](../../docs/requirements.md) — the audience and segment requirements this
  engine satisfies.
