-- CW-0005: dense channel ordinals (Unit 1), segments (Unit 2's subject), the generation-versioned
-- forward index (Unit 2, Unit 4), and the global generation pointer the batch swap advances
-- atomically (Unit 4). The reverse index (Unit 3) lives in Redis, not here — see
-- internal/membership/reverse.

-- A sequence rather than a serial column on channel_ordinals directly, so retiring a channel (a
-- soft delete: the row stays, marked retired) can never free its ordinal for reuse — nextval() only
-- ever goes up, regardless of which rows are later marked retired.
CREATE SEQUENCE channel_ordinal_seq;

CREATE TABLE channel_ordinals (
    channel_id    uuid PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    ordinal       bigint NOT NULL DEFAULT nextval('channel_ordinal_seq'),
    retired       boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX channel_ordinals_ordinal_idx ON channel_ordinals (ordinal);

-- Segment identifiers are dense ordinals too (CW-0005 Unit 3): the reverse index's per-channel
-- bitmap holds segment ordinals, not segment uuids, for the same compactness reason channel
-- ordinals exist.
CREATE SEQUENCE segment_ordinal_seq;

CREATE TABLE segments (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ordinal             bigint NOT NULL DEFAULT nextval('segment_ordinal_seq'),
    name                text NOT NULL,
    predicate_source    text NOT NULL,
    -- The protojson-encoded checked CEL tree (CW-0004 Unit 2's storage format), not re-parsed from
    -- predicate_source at read time.
    predicate_tree      jsonb NOT NULL,
    -- 'incremental': eligible for Unit 5's per-channel incremental path. 'batch_only': excluded
    -- because its predicate reads an event aggregate, whose value can change with the passage of
    -- time alone (CW-0004 Unit 5's windowed conditions), not just an attribute write.
    refresh_mode        text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT segments_refresh_mode_check CHECK (refresh_mode IN ('incremental', 'batch_only'))
);

CREATE UNIQUE INDEX segments_ordinal_idx ON segments (ordinal);

-- A singleton row (enforced by the boolean primary key, which admits only one value) holding the
-- generation every reader's forward-index query joins against. Advancing it is one UPDATE, which
-- Postgres commits atomically alongside the new generation's segment_membership rows in the same
-- transaction — the property Unit 4's torn-read prevention depends on.
CREATE TABLE membership_generation (
    id            boolean PRIMARY KEY DEFAULT true CHECK (id),
    generation    bigint NOT NULL DEFAULT 0
);

INSERT INTO membership_generation (generation) VALUES (0);

-- One row per segment per generation it was computed for. Never updated in place by a batch
-- recomputation — a new generation is a new row — so an old generation stays intact for revert
-- until Unit 4's reclamation deletes it. Unit 5's incremental path is the one exception: it updates
-- the current generation's row in place, since a single channel's bit flip needs none of the
-- multi-segment atomicity a full batch swap protects.
CREATE TABLE segment_membership (
    segment_id    uuid NOT NULL REFERENCES segments (id) ON DELETE CASCADE,
    generation    bigint NOT NULL,
    -- A serialized roaring bitmap (Bitmap.ToBytes) of channel ordinals.
    bitmap        bytea NOT NULL,
    computed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (segment_id, generation)
);
