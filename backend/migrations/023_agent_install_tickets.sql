-- 023 — Single-use agent install downloads.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- The agent install URL used to carry the cluster's tunnel credential in its
-- path, so a URL left in shell history or a CI log was a working agent
-- credential for the life of the install. It now carries a download ticket:
-- minted each time an administrator opens the install package, redeemed by the
-- first fetch with one `DELETE ... RETURNING`, and dead after fifteen minutes
-- unused. Only the ticket's SHA-256 is stored. Rotating a cluster's agent token
-- deletes every ticket outstanding for it in the same transaction.
CREATE TABLE IF NOT EXISTS agent_install_tickets (
    token_hash VARCHAR(64) PRIMARY KEY,
    cluster_id BIGINT      NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_install_tickets_cluster_id ON agent_install_tickets (cluster_id);
CREATE INDEX IF NOT EXISTS idx_agent_install_tickets_expires_at ON agent_install_tickets (expires_at);
