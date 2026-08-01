-- The minimal channel attribute store CW-0004's set-wise evaluator (Unit 4) and reach estimator
-- (Unit 6) run against. This is not the channel/user schema CW-0010 lists among PostgreSQL's
-- responsibilities — that full domain model (channels, users, tags, event history) belongs to
-- whichever future item owns channel and user management. attributes is a single jsonb document
-- keyed by attribute name because the registry (CW-0004 Unit 1) is configuration, not a migration:
-- a fixed column per attribute would put a schema change behind every new attribute, defeating the
-- registry's whole purpose.
CREATE TABLE channels (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    attributes    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Read by sqlcompile's compiled WHERE clauses on every attribute key they reference.
CREATE INDEX channels_attributes_idx ON channels USING gin (attributes);

-- The SQL-side twin of registry.NormalizeSemVer (internal/audience/registry/semver.go): zero-pads
-- up to three dot-separated components to 10 digits each, so plain lexical comparison on the result
-- matches numeric version ordering. sqlcompile only calls this for a semver-typed attribute
-- reference; a semver() call on a literal is normalized in Go at compile time instead, so this
-- function only ever sees real channel data, not campaign-author input.
CREATE FUNCTION semver_normalize(v text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
    SELECT
        lpad(COALESCE(parts[1], '0'), 10, '0') || '.' ||
        lpad(COALESCE(parts[2], '0'), 10, '0') || '.' ||
        lpad(COALESCE(parts[3], '0'), 10, '0')
    FROM (SELECT regexp_split_to_array(v, '\.') AS parts) AS s;
$$;
