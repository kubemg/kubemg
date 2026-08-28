-- 018 — where a call came from.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- `audit_events` carried twenty-four columns describing who did what to which
-- object, and nothing at all about where the call came from — so "from where",
-- the second question in any SOC 2 or ISO 27001 walkthrough, had no answer in
-- the schema. These two columns are that answer, and they are added ahead of the
-- console surface that reads them because they are the part that **cannot be
-- backfilled**: a call already made has no address left to go and find.
--
-- Both are nullable and both are empty on:
--
--   * every row written before this migration — genuinely not recorded, which
--     the console states as such rather than drawing a blank that reads as an
--     unknown host;
--   * every record with no caller, which is a real and ordinary state — the JIT
--     expirer closing out a grant and the alarm poller reading events are things
--     this server did on its own, and inventing an address for them would be the
--     more misleading choice.
--
-- `source_addr` is what the server resolved the client to, through the proxy
-- headers it has been configured to trust, and never carries a port: a source
-- port names a socket that closed seconds later, and printing one in an audit
-- column invites it to be read as meaningful. It is indexed because "everything
-- from this address" is a question asked during an incident, when the answer is
-- wanted in one query rather than a table scan.
--
-- `user_agent` is the client's own claim about what it is, truncated to 256
-- characters on the way in. It is untrusted by construction and worth keeping
-- for exactly that reason: a credential being used by something that is not what
-- it was issued to shows up here first. It is not indexed — nobody filters on
-- it, they read it on a record.

ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS source_addr VARCHAR(64);
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS user_agent TEXT;

CREATE INDEX IF NOT EXISTS idx_audit_events_source_addr ON audit_events (source_addr);
