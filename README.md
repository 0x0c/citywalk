**English** · [日本語](README-ja.md)

# citywalk

This repository holds the design record for citywalk's server backend. The repository carries two
kinds of document and no application code. One is a catalog of the requirements the backend must
meet. The other is a set of roadmap items, one per design decision behind it. The code comes later,
and it follows what these documents settle.

citywalk is a walking application for iOS and Android. Its backend takes on one subject first, an
**in-app message platform**. The platform draws contextual messages over the application's own
screens. Each message takes the form of a dialog, a banner, or a full-screen panel. A push
notification pulls a user back from outside the application. An in-app message instead reaches a
user who already has the application open. The message reacts to that user's current activity.

The platform exists because the alternative costs a release. A campaign built into the application
costs a code change, a review, and weeks of waiting. Wrong copy stays wrong for a fortnight, and a
campaign that should stop cannot stop. A platform inverts that relationship. The application ships
one renderer, and every campaign after that is data.

## Repository layout

```
docs/
  requirements.md      the requirement catalog, with an identifier per requirement
  ja/requirements.md   the Japanese counterpart
roadmaps/
  README.md            how a roadmap item works, plus the index of the items written so far
  CW-0001-in-app-message-platform-scope/
    CW-0001-in-app-message-platform-scope.md      English
    CW-0001-in-app-message-platform-scope-ja.md   Japanese
.agent-workflows/      the prose norm every document here follows, its textlint runtime, and the
                        implement workflow that turns a roadmap item into code
.claude/skills/        the same workflows, exposed to Claude Code as skills
```

## Where to start

1. [`docs/requirements.md`](docs/requirements.md) states what the platform must do, not how.
   Functional requirements take identifiers of the form `FR-<area>-<number>`. Non-functional
   requirements take `NFR-<area>-<number>`. Roadmap items and test cases both reference those
   identifiers.
2. [CW-0001](roadmaps/CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md)
   divides the platform into five services. The axis of division is how fast the data each service
   owns changes. CW-0001 also names the item that designs each piece.
3. [`roadmaps/README.md`](roadmaps/README.md) indexes every item, defines the format the items
   follow, and gives the procedure for adding one.
4. [`docs/demo.md`](docs/demo.md) runs the phase-one server some of these items already describe.
   It calls every network endpoint that server exposes today, against a disposable PostgreSQL and
   Redis.

## Bilingual documentation

Every document here exists in English and in Japanese. A change to one language lands together with
the change to the other.

The naming convention differs by directory. Under `docs/`, the Japanese file mirrors the English
path beneath `docs/ja/`. Under `roadmaps/`, the Japanese file sits beside the English one, with a
`-ja` suffix on the slug. Each file opens with a switcher line linking to its counterpart.

A Japanese file is not a mechanical translation. The Japanese file makes the same argument in the
same structure. Its prose reads naturally in Japanese rather than tracking the English sentence by
sentence.

## The writing norm

The documents here make an argument that a reader must be able to follow. The norm that shapes that
prose has three layers.

- [`document-writing`](.agent-workflows/document-writing/workflow.md) — the principles that hold
  for both languages.
- [`english-document-writing`](.agent-workflows/english-document-writing/workflow.md) — the
  English mechanics.
- [`japanese-document-writing`](.agent-workflows/japanese-document-writing/workflow.md) — the
  mechanics for Japanese prose.

Read the norm before drafting: the norm shapes a draft rather than proofreading one afterward.

After drafting, run [textlint](https://github.com/textlint/textlint) over the files you touched, and
revise until the tool reports nothing. The runtime and the configuration live under
[`.agent-workflows/document-writing/textlint/`](.agent-workflows/document-writing/textlint/), and
they need node and npm.

```bash
SKILL_DIR=.agent-workflows/document-writing
npm --prefix "$SKILL_DIR/textlint" ci --ignore-scripts
npx --prefix "$SKILL_DIR/textlint" textlint \
  --config "$SKILL_DIR/textlint/.textlintrc.json" \
  docs/requirements.md
```

## Implementing an item

Turning a roadmap item's `Detailed design` into code follows its own guardrailed workflow:
[`implement`](.agent-workflows/implement/workflow.md). It resolves the item, plans the change and
waits for approval before writing code, implements against the stack
[CW-0010](roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md) settled,
and keeps reviewing and fixing — a mechanical gate, then a semantic self-review — until nothing is
left open, before it updates the item's `Status` and ships.

## Status

Every roadmap item stands at `Proposal`: the design is under consideration, and no code has shipped.
Three assumptions shape the design. The backend language is Go. The target scale is 1 million to 10 million
monthly active users. The clients are native iOS and Android applications that keep working while
offline.

The client library that draws these messages on the device falls outside this repository. This
repository defines the responsibilities the platform requires of that library.
