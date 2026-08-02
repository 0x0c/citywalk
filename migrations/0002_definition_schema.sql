-- The definition service's five entities (CW-0003 Unit 1), split per Unit 3's rule: a field goes in
-- a column when something filters, sorts, or joins on it, and in a JSON document when it is only
-- ever read whole. Content is the only field that rule sends to JSON — everything else here is
-- relational.

CREATE TABLE messages (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text NOT NULL,
    state             text NOT NULL,
    priority          integer NOT NULL DEFAULT 0,
    window_start      timestamptz NOT NULL,
    window_end        timestamptz NOT NULL,
    -- The audience reference never leaves the server (CW-0002's delivery boundary): this column has
    -- no counterpart anywhere in the payload the delivery service assembles.
    audience_ref      text,
    holdout_fraction  double precision NOT NULL DEFAULT 0,
    conversion_event  text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_state_check
        CHECK (state IN ('draft', 'scheduled', 'active', 'paused', 'completed', 'archived')),
    CONSTRAINT messages_window_check CHECK (window_start < window_end)
);

-- The delivery service filters on state and orders by priority on every payload assembly, so both
-- want an index; the definition service's own reads are by primary key.
CREATE INDEX messages_state_idx ON messages (state);
CREATE INDEX messages_priority_idx ON messages (priority);

-- One-to-one with messages, its own table per CW-0003 Unit 1's five-entity split rather than columns
-- on messages: ControlPolicy is a distinct entity, and CW-0007's governance work reads it without
-- needing the rest of the message row.
CREATE TABLE control_policies (
    message_id                uuid PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    per_message_cap           integer NOT NULL,
    min_interval_seconds      integer NOT NULL,
    exempt_from_project_cap   boolean NOT NULL DEFAULT false
);

-- Content is the one field CW-0003 Unit 3 sends to JSON: the delivery service copies it into a
-- payload whole and never queries inside it, so a relational decomposition of the tagged union
-- would buy a join per read for no gain.
CREATE TABLE variants (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id    uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    weight        integer NOT NULL,
    language      text NOT NULL,
    content       jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX variants_message_id_idx ON variants (message_id);
-- The delivery service selects a variant by language first (CW-0003 Unit 1), so the pair is the
-- lookup this index serves.
CREATE INDEX variants_message_id_language_idx ON variants (message_id, language);

CREATE TABLE triggers (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id         uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    kind               text NOT NULL,
    occurrence_goal    integer NOT NULL,
    -- CEL (CW-0010 Unit 2), evaluated entirely on the device (CW-0002); empty means no property
    -- condition beyond the trigger firing at all.
    event_predicate    text NOT NULL DEFAULT ''
);

CREATE INDEX triggers_message_id_idx ON triggers (message_id);

CREATE TABLE display_conditions (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id               uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    delay_seconds            integer NOT NULL DEFAULT 0,
    screen_filter_mode       text NOT NULL,
    screens                  text[] NOT NULL DEFAULT '{}',
    connectivity_required    text NOT NULL,
    CONSTRAINT display_conditions_screen_filter_mode_check
        CHECK (screen_filter_mode IN ('allow', 'deny')),
    CONSTRAINT display_conditions_connectivity_required_check
        CHECK (connectivity_required IN ('any', 'online'))
);

CREATE INDEX display_conditions_message_id_idx ON display_conditions (message_id);
