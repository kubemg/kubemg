-- 021 — WebSocket tickets, shared by every replica.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- The in-page terminal and the browser shell authenticate their WebSocket with
-- a single-use ticket, minted by one ordinary request and redeemed by the
-- upgrade that follows. Held in process memory, a ticket minted on one replica
-- was refused by any other, and a load balancer owes the two requests no
-- affinity. A row here is visible to every replica, and redeeming it is one
-- `DELETE ... RETURNING`, so exactly one caller wins a ticket presented twice.
--
-- Only the ticket's SHA-256 is stored. Rows live twenty seconds; expired ones
-- are cleared whenever the next ticket is minted.
CREATE TABLE IF NOT EXISTS ws_tickets (
    token_hash VARCHAR(64) PRIMARY KEY,
    claims     BYTEA       NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ws_tickets_expires_at ON ws_tickets (expires_at);
