-- CW-0008 Unit 2's per-experiment salt, generated once at insert and never reused. Generating it as
-- a column default rather than in Go application code means every existing call to
-- store.InsertMessage gets a real, unique salt with no code change: gen_random_bytes needs pgcrypto,
-- already enabled by migrations/0001_extensions.sql.
ALTER TABLE messages
    ADD COLUMN experiment_salt text NOT NULL DEFAULT encode(gen_random_bytes(16), 'hex');
