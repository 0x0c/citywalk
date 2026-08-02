-- CW-0010 Unit 9 gives the administrative API a real authenticated principal for the first time
-- (previously message_audit_log had no caller identity to record). actor carries that principal's
-- subject, closing admin.proto's AdminService doc comment's claim that the audit log "names the
-- actor" — until now nothing populated it.
ALTER TABLE message_audit_log ADD COLUMN actor text NOT NULL DEFAULT '';
