-- CW-0009 Unit 3's rollup-write idempotency key: applyBatch (internal/event/consumer) inserts one row
-- here, in the same transaction as every rollup increment it makes for an event, before touching
-- targeting_rollup, campaign_rollup, reach_sketch, or suppression_rollup. Those four tables increment
-- unconditionally (count = count + 1), so on their own they cannot tell a first delivery from a
-- replay; this table is what makes that distinction. It is keyed by event id alone, not per rollup
-- table, because a given event id always resolves to the same fixed set of rollup writes — a function
-- of that event's own kind and message_id, both fixed when the event was created — so one row per
-- event is enough to dedup every rollup write it drives together.
--
-- Both RunOnce (Postgres-backed: its offset advance and its rollup writes already share one
-- transaction) and RunOnceFromLog (log-backed: its consumer-group offset commit is a second system
-- that can fail independently of the Postgres commit) share this table through the same applyBatch, so
-- a replayed batch is a no-op for any event already counted, regardless of which system failed to
-- commit.
--
-- Deliberately not pruned. A bounded, pruned log is the right shape for a change feed a reader falls
-- back to a full resync from once its cursor is too old (CW-0006 Unit 3's design for exactly that
-- shape). This table protects something different: a rollup write must never be double-applied, no
-- matter how long after the fact a replay happens — a consumer-group offset reset to an old position
-- is a deliberate operational action, not a routine event, and pruning would silently reopen the
-- double-count this table exists to prevent for any event older than the retention window. Its
-- per-row footprint (one uuid, one timestamp) also grows at exactly the same rate events_log already
-- does, and that table is kept unpruned in phase one already, so this adds no new order of growth.
CREATE TABLE rollup_applied_events (
    event_id     uuid PRIMARY KEY,
    applied_at   timestamptz NOT NULL DEFAULT now()
);
