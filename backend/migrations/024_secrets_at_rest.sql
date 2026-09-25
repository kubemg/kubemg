-- 024 — Credentials encrypted at rest.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- Every credential KubeMG has to read back is now stored as
-- `enc:v1:<base64(nonce || AES-256-GCM ciphertext || tag)>` under the key in
-- KUBEMG_SECRET_KEY:
--
--   server_secrets.value                  (the JWT signing key)
--   clusters.agent_token                  (the agent tunnel credential)
--   clusters.service_account_token        (direct mode)
--   observability_sources.credential
--   helm_repositories.credential
--   alarm_channels.secret
--   sso_providers.client_secret
--   sso_providers.ldap_bind_password
--
-- The encryption itself is not DDL: the server encrypts every value still in
-- the clear at boot, in place, when a key is set, and skips values that already
-- carry the prefix. Nothing here can do that step — the key never reaches the
-- database.
--
-- The agent token is encrypted rather than hashed because the install sheet
-- re-renders the agent package from it without rotating it. A handshake can no
-- longer find a cluster by the token itself (ciphertext under a random nonce is
-- not searchable), so it looks the row up by the token's SHA-256 and compares
-- the decrypted token in constant time. The server backfills the hash at boot.
ALTER TABLE clusters ALTER COLUMN agent_token TYPE TEXT;
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS agent_token_hash VARCHAR(64);
CREATE INDEX IF NOT EXISTS idx_clusters_agent_token_hash ON clusters (agent_token_hash);
DROP INDEX IF EXISTS idx_clusters_agent_token;
