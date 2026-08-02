-- Continuation of 0011_job_queue.sql: see that file's header for why this is a separate migration
-- (Postgres forbids using an enum value, here river_job_state's 'pending', within the same
-- transaction that added it). This file is river's migration line "main" versions 004 (the remainder,
-- after the enum addition) through 007.

-- 004_pending_and_more.up.sql (part 2: everything after the enum addition)
ALTER TABLE river_job DROP CONSTRAINT finalized_or_finalized_at_null;
ALTER TABLE river_job ADD CONSTRAINT finalized_or_finalized_at_null CHECK (
    (finalized_at IS NULL AND state NOT IN ('cancelled', 'completed', 'discarded')) OR
    (finalized_at IS NOT NULL AND state IN ('cancelled', 'completed', 'discarded'))
);

DROP TRIGGER river_notify ON river_job;
DROP FUNCTION river_job_notify;

CREATE TABLE river_queue (
    name text PRIMARY KEY NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}' ::jsonb,
    paused_at timestamptz,
    updated_at timestamptz NOT NULL
);

ALTER TABLE river_leader
    ALTER COLUMN name SET DEFAULT 'default',
    DROP CONSTRAINT name_length,
    ADD CONSTRAINT name_length CHECK (name = 'default');

-- 005_migration_unique_client.up.sql
DO
$body$
BEGIN
    IF (SELECT to_regclass('river_migration') IS NOT NULL) THEN
        ALTER TABLE river_migration
            RENAME TO river_migration_old;

        CREATE TABLE river_migration(
            line TEXT NOT NULL,
            version bigint NOT NULL,
            created_at timestamptz NOT NULL DEFAULT NOW(),
            CONSTRAINT line_length CHECK (char_length(line) > 0 AND char_length(line) < 128),
            CONSTRAINT version_gte_1 CHECK (version >= 1),
            PRIMARY KEY (line, version)
        );

        INSERT INTO river_migration
            (created_at, line, version)
        SELECT created_at, 'main', version
        FROM river_migration_old;

        DROP TABLE river_migration_old;
    END IF;
END;
$body$
LANGUAGE 'plpgsql';

ALTER TABLE river_job
    ADD COLUMN IF NOT EXISTS unique_key bytea;

CREATE UNIQUE INDEX IF NOT EXISTS river_job_kind_unique_key_idx ON river_job (kind, unique_key) WHERE unique_key IS NOT NULL;

CREATE UNLOGGED TABLE river_client (
    id text PRIMARY KEY NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}',
    paused_at timestamptz,
    updated_at timestamptz NOT NULL,
    CONSTRAINT name_length CHECK (char_length(id) > 0 AND char_length(id) < 128)
);

CREATE UNLOGGED TABLE river_client_queue (
    river_client_id text NOT NULL REFERENCES river_client (id) ON DELETE CASCADE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    max_workers bigint NOT NULL DEFAULT 0,
    metadata jsonb NOT NULL DEFAULT '{}',
    num_jobs_completed bigint NOT NULL DEFAULT 0,
    num_jobs_running bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (river_client_id, name),
    CONSTRAINT name_length CHECK (char_length(name) > 0 AND char_length(name) < 128),
    CONSTRAINT num_jobs_completed_zero_or_positive CHECK (num_jobs_completed >= 0),
    CONSTRAINT num_jobs_running_zero_or_positive CHECK (num_jobs_running >= 0)
);

-- 006_bulk_unique.up.sql
CREATE OR REPLACE FUNCTION river_job_state_in_bitmask(bitmask BIT(8), state river_job_state)
RETURNS boolean
LANGUAGE SQL
IMMUTABLE
AS $$
    SELECT CASE state
        WHEN 'available' THEN get_bit(bitmask, 7)
        WHEN 'cancelled' THEN get_bit(bitmask, 6)
        WHEN 'completed' THEN get_bit(bitmask, 5)
        WHEN 'discarded' THEN get_bit(bitmask, 4)
        WHEN 'pending'   THEN get_bit(bitmask, 3)
        WHEN 'retryable' THEN get_bit(bitmask, 2)
        WHEN 'running'   THEN get_bit(bitmask, 1)
        WHEN 'scheduled' THEN get_bit(bitmask, 0)
        ELSE 0
    END = 1;
$$;

ALTER TABLE river_job ADD COLUMN IF NOT EXISTS unique_states BIT(8);

CREATE UNIQUE INDEX IF NOT EXISTS river_job_unique_idx ON river_job (unique_key)
    WHERE unique_key IS NOT NULL
      AND unique_states IS NOT NULL
      AND river_job_state_in_bitmask(unique_states, state);

DROP INDEX river_job_kind_unique_key_idx;

-- 007_notification_outbox_sqlite_jsonb_and_sql_cleanup.up.sql
CREATE TABLE river_notification (
    id bigserial PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload text NOT NULL,
    topic text NOT NULL,
    CONSTRAINT topic_length CHECK (length(topic) > 0 AND length(topic) < 128)
);

CREATE INDEX river_notification_created_at_idx ON river_notification (created_at);
CREATE INDEX river_notification_topic_id_idx ON river_notification (topic, id);

DROP TABLE river_client_queue;
DROP TABLE river_client;

ALTER TABLE river_job
    ALTER COLUMN max_attempts SET DEFAULT 25;

ALTER TABLE river_queue
    ALTER COLUMN updated_at SET DEFAULT CURRENT_TIMESTAMP;

-- Record every line version as applied, matching what river's own migrator (rivermigrate) would have
-- written to river_migration had it run these instead of this file and 0011 — river's client
-- validates on startup that river_migration's "main" line is present and current, and would
-- otherwise try to apply these same migrations itself.
INSERT INTO river_migration (line, version) VALUES
    ('main', 1), ('main', 2), ('main', 3), ('main', 4), ('main', 5), ('main', 6), ('main', 7);
