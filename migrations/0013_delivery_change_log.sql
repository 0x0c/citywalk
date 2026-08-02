-- CW-0006 Unit 3's bounded change log: one row per message whose eligibility state changed (a kill
-- switch transition crossing the "active" boundary — the only mutation surface FR-MSG-01's
-- forward-only states give the administrative API today; see internal/platform/connectserver/admin.go).
-- seq is a monotonically increasing cursor value a device's delta-sync cursor is checked against.
-- kind records the direction of the transition that produced the row purely for a human reading the
-- table — a delta sync decides upsert vs. tombstone by checking the changed message against the
-- freshly computed eligible set, not by trusting kind, so an imprecise label here costs nothing.
CREATE TABLE delivery_change_log (
    seq          bigserial PRIMARY KEY,
    message_id   uuid NOT NULL REFERENCES messages (id),
    kind         text NOT NULL CHECK (kind IN ('upsert', 'tombstone')),
    occurred_at  timestamptz NOT NULL DEFAULT now()
);

-- Every delta sync's cursor-honorability check is "is this cursor's issued_at within the retention
-- window", not a row lookup, so the index this table actually needs is Since's own query shape
-- (every row after a given seq) plus Prune's (every row older than a cutoff).
CREATE INDEX delivery_change_log_occurred_at_idx ON delivery_change_log (occurred_at);
