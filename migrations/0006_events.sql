-- CW-0009's event pipeline. Phase one runs without a partitioned log or a separate columnar store
-- (CW-0010's single-process scope): events_log plays both roles at once — the durable, per-channel
-- ordered seam (Unit 3) and the deduplicated store consumers aggregate from (Unit 4). A later pass
-- that introduces a real log (Kafka/Kinesis) and columnar store (ClickHouse/similar) replaces this
-- table's two roles with the real ones without changing the Go-level Event envelope or the rollup
-- tables downstream of it.
CREATE TABLE events_log (
    -- The client-generated UUIDv7 (Unit 1). PRIMARY KEY doubles as Unit 4's merge-time dedup: a
    -- resend of the same id is a no-op insert (see ingest.Accept's ON CONFLICT DO NOTHING), not a
    -- read-before-write check on the ingestion path.
    id                   uuid PRIMARY KEY,
    -- Global receipt order. Consumers page through events_log by this column rather than by
    -- device_time (client-controlled, not monotonic) — it is the phase-one substitute for a
    -- partition offset (Unit 3).
    seq                  bigserial NOT NULL,
    channel_id           uuid NOT NULL REFERENCES channels (id),
    kind                 text NOT NULL,
    name                 text NOT NULL DEFAULT '',
    device_time          timestamptz NOT NULL,
    server_time          timestamptz NOT NULL DEFAULT now(),
    properties           jsonb NOT NULL DEFAULT '{}'::jsonb,
    message_id           uuid REFERENCES messages (id),
    variant_id           uuid REFERENCES variants (id),
    suppression_reason   text
);

-- Consumers scan in receipt order; this is the one index every rollup consumer's catch-up query uses.
CREATE UNIQUE INDEX events_log_seq_idx ON events_log (seq);
-- Attribution (Unit 6) scans a channel's events in device-time order to find the impression
-- preceding a conversion.
CREATE INDEX events_log_channel_device_time_idx ON events_log (channel_id, device_time);
-- The campaign and suppression rollups, and attribution, all filter by message_id first.
CREATE INDEX events_log_message_id_idx ON events_log (message_id) WHERE message_id IS NOT NULL;

-- Unit 3's per-consumer position: a consumer's next catch-up query starts just past last_seq, and a
-- consumer may be replayed by resetting its row (or deleting it, to replay from the beginning).
CREATE TABLE event_consumer_offsets (
    consumer_name    text PRIMARY KEY,
    last_seq         bigint NOT NULL DEFAULT 0,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- The targeting rollup (Unit 5): what CW-0004's windowed event-aggregate conditions ("opened this
-- screen three times in the last week") sum over. One row per channel, event name, and day.
CREATE TABLE targeting_rollup (
    channel_id    uuid NOT NULL,
    event_name    text NOT NULL,
    day           date NOT NULL,
    count         bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, event_name, day)
);

-- The campaign rollup (Unit 5): what a report reads. One row per message, variant, hour, and event
-- kind; exact, since these are sums and cost nothing to keep exact (impressions, clicks, dismissals,
-- auto-closes are all counted here, never estimated).
CREATE TABLE campaign_rollup (
    message_id    uuid NOT NULL,
    variant_id    uuid NOT NULL,
    hour          timestamptz NOT NULL,
    kind          text NOT NULL,
    count         bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id, variant_id, hour, kind)
);

CREATE INDEX campaign_rollup_message_id_idx ON campaign_rollup (message_id);

-- Unique reach (Unit 5): a HyperLogLog sketch per message, variant, and day, merged across days by
-- the reach package rather than rescanned from raw events. sketch is the serialized register set
-- (internal/event/hll).
CREATE TABLE reach_sketch (
    message_id    uuid NOT NULL,
    variant_id    uuid NOT NULL,
    day           date NOT NULL,
    sketch        bytea NOT NULL,
    PRIMARY KEY (message_id, variant_id, day)
);

-- The suppression report (Unit 7): a per-campaign, per-reason breakdown, alongside the impression
-- counts campaign_rollup already carries.
CREATE TABLE suppression_rollup (
    message_id    uuid NOT NULL,
    reason        text NOT NULL,
    day           date NOT NULL,
    count         bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id, reason, day)
);

-- CW-0009 Unit 6's conversion attribution rule counts one conversion per campaign per window per
-- channel; this table is the record of an attribution already made, so a re-run of the attribution
-- job over the same window does not double count (the columnar-store-idempotency this design relies
-- on elsewhere, applied here to a derived rather than raw event). variant_id is text, not uuid: a
-- channel attributed to the holdout carries attribution.HoldoutVariantID, a sentinel string rather
-- than a real variant, so the column cannot be constrained to valid UUIDs the way reach_sketch's can.
CREATE TABLE conversion_attributions (
    message_id      uuid NOT NULL REFERENCES messages (id),
    channel_id      uuid NOT NULL REFERENCES channels (id),
    conversion_id   uuid NOT NULL,
    variant_id      text NOT NULL,
    attributed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, channel_id, conversion_id)
);

CREATE INDEX conversion_attributions_message_variant_idx ON conversion_attributions (message_id, variant_id);
