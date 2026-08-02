**English** · [日本語](ja/demo.md)

# Running the demo

This document walks through `cmd/demo`. The program exercises the phase-one server
[CW-0010](../roadmaps/CW-0010-runtime-technology-stack/CW-0010-runtime-technology-stack.md) Unit 11
describes. That server shares one PostgreSQL instance and one Redis instance. The definition,
audience, and delivery services all run against them.

`cmd/demo` registers a device channel and defines an audience segment. It then authors and publishes
a message, synchronizes it, confirms it, and reports on it. Each step is a call a real mobile client
or a real campaign author would make on its own. The program runs every call back to back against a
disposable database.

Reading this document requires no prior familiarity with the rest of the repository.

## Prerequisites

- [Go](https://go.dev/), at the version [`go.mod`](../go.mod) names.
- [Docker](https://www.docker.com/) with the `docker compose` plugin, to run PostgreSQL and Redis.
  Any other way of running PostgreSQL 16 and Redis 7 on `localhost` works too. Adjust the
  `-postgres-dsn` and `-redis-addr` flags in the next section to match.

## Running it

Start PostgreSQL and Redis, then run the program:

```bash
make demo-up   # docker compose -f deploy/docker-compose.yml up -d --wait
make demo      # go run ./cmd/demo
```

`cmd/demo` prints one numbered step per network call it makes. Each step's result follows right
after. The program applies every migration under [`migrations/`](../migrations) itself. A separate
migration step is not needed.

After the walkthrough finishes, the server keeps running at `http://127.0.0.1:8085`. It also prints a
`curl` command against the channel it registered. A reader can use that command to keep exploring by
hand. Press Ctrl-C to stop the server, then tear the containers down:

```bash
make demo-down  # docker compose -f deploy/docker-compose.yml down -v
```

`make demo` can run again while `make demo-up`'s containers are still up. The program clears out an
earlier run's tables and Redis keys first. Every run reproduces the same result.

## What each step demonstrates

- **Registering a device channel** calls `ChannelService.Register` with one attribute,
  `country: "JP"`. The response carries a channel identifier and an access token, the credential
  every later call in the walkthrough presents as a bearer token. This stands in for a mobile
  client's first launch.
- **Defining an audience segment** saves a segment named "Japan travelers" with the predicate
  `country == "JP"`, then computes which channels belong to it. This step calls the audience and
  membership packages directly, not through a network endpoint:
  [CW-0001](../roadmaps/CW-0001-in-app-message-platform-scope/CW-0001-in-app-message-platform-scope.md)
  has not yet given segment authoring an administrative endpoint of its own. `AdminService`'s
  [proto definition](../proto/citywalk/admin/v1/admin.proto) documents that same gap.
- **Authoring and publishing a message** calls `AdminService.CreateMessage` with a dialog variant.
  That variant targets the segment above. `AdminService.UpdateMessageState` then moves the message
  from `draft` to `active`. [FR-MSG-01](requirements.md) fixes that direction: a message's state
  never moves backward. `AdminService.ListAuditLog` reads the transition back, naming who made it and
  when.
- **Synchronizing the channel's payload** calls `DeliveryService.Sync` twice. The first call carries
  no prior state and returns the message as one entry. The second carries the etag the first call
  returned. It gets back `unchanged: true` with no entries. The pair demonstrates
  [CW-0006](../roadmaps/CW-0006-payload-delta-sync/CW-0006-payload-delta-sync.md)'s conditional
  request. An unchanged payload costs a cache lookup, not a full payload assembly.
- **Confirming and submitting an event** calls `DeliveryService.Confirm`. That is the server-side
  re-check a device makes right before it displays a message. `EventService.Submit` then records an
  impression against the message and the variant `Sync` delivered.

## Troubleshooting

- `connect to postgres ...` — PostgreSQL is not reachable at the configured connection string.
  Confirm `make demo-up` finished. `docker compose -f deploy/docker-compose.yml ps` should show both
  services as healthy. If something else already occupies port 5432, pass `-postgres-dsn` with a
  different one.
- `sync returned N entries, want 1` — this should not happen. `cmd/demo` clears its own tables and
  Redis keys at the start of every run. If it does, something else wrote to the same database or
  Redis instance at the same time. Run `make demo-down`, then `make demo-up`, to start from an empty
  database. Also check that no other process shares the configured `-postgres-dsn` or `-redis-addr`.
