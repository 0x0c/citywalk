-- CW-0001 Unit 1's audit log: one row per state transition a message goes through after creation —
-- the trail an administrator needs to see who stopped or resumed a campaign, and when. It does not
-- yet cover every field a definition can change (content, triggers, display conditions); state is
-- the one mutation this pass gives the administrative surface a real function for
-- (store.UpdateState, CW-0001 Unit 1's kill switch).
CREATE TABLE message_audit_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id    uuid NOT NULL REFERENCES messages (id),
    from_state    text NOT NULL,
    to_state      text NOT NULL,
    occurred_at   timestamptz NOT NULL DEFAULT now()
);

-- The one read this table serves: an administrator reviewing one campaign's history, newest first.
CREATE INDEX message_audit_log_message_id_idx ON message_audit_log (message_id, occurred_at DESC);
