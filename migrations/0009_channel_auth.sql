-- CW-0010 Unit 9's device-bound credential, and CW-0003 Unit 4's per-device schema major
-- declaration (FR-API-01: a device registers and receives an identifier and a credential, sending
-- its application version and other attributes in the same call — supported_schema_major is the one
-- piece of that call that is delivery-protocol metadata rather than an audience-predicate attribute,
-- so unlike locale/timezone/app_version it gets its own column rather than living in the attributes
-- jsonb document).
--
-- credential_hash is nullable: a channel row created directly (every audience/membership/delivery
-- integration test that inserts one without going through register.Register, plus any channel that
-- predates this migration) has no credential and can never authenticate as a device — that is
-- correct, since those rows exist only to be looked up server-side, never to receive a token.
ALTER TABLE channels
    ADD COLUMN credential_hash bytea,
    ADD COLUMN supported_schema_major integer NOT NULL DEFAULT 1;
