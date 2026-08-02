-- CW-0002 Unit 2 and Unit 4 need two fields CW-0003's schema didn't carry yet: a version counter a
-- device can use to detect a changed message, and the per-message flag that opts a campaign into
-- the server confirmation escape hatch.

ALTER TABLE messages
    ADD COLUMN version integer NOT NULL DEFAULT 1;

ALTER TABLE control_policies
    ADD COLUMN requires_server_confirmation boolean NOT NULL DEFAULT false;
