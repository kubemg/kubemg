-- 015 — programmatic access: machine accounts and their credentials.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- A machine account — a CI pipeline's release stage, a release bot — is a row in
-- `users`, not a table of its own. Every grant, every namespace scope, the
-- permissions matrix, the audit trail and the proxy's own impersonation are keyed
-- on a user id; a second kind of principal would mean teaching all of them a
-- second shape for an identity that needs exactly the access model a developer
-- needs. `account_type` is what separates the two, and it defaults to 'user', so
-- every account that predates this is a person.
--
-- What is genuinely new is the credential. A generated kubeconfig carries a
-- proxy-scoped JWT, which is right for a file on a laptop that expires within the
-- day and wrong for one a pipeline holds for months: a JWT is stateless, so it
-- cannot be withdrawn before its own expiry. A row can. `machine_tokens` holds
-- one row per issued credential; revoking is a write, and it takes effect on the
-- credential's next call.
--
-- Only the hash is stored. The secret is 256 bits of CSPRNG output, so there is
-- nothing to guess and a password KDF would put its work factor in front of every
-- proxied call; `hint` is the token's opening characters, which is what lets an
-- operator match a value in a CI secret store to the row they are about to
-- revoke.
--
-- `expires_at` is nullable on purpose: a credential with no expiry is allowed,
-- because a release pipeline that stops at 3am on a quarter boundary is an outage
-- nobody scheduled. `last_used_at` is what replaces the clock as a control — it
-- is written at most once every few minutes rather than per call, since this read
-- sits in front of everything a pipeline does.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS account_type VARCHAR(20) NOT NULL DEFAULT 'user';

CREATE TABLE IF NOT EXISTS machine_tokens (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT       NOT NULL,
    cluster_id   BIGINT       NOT NULL,
    name         VARCHAR(120) NOT NULL,
    namespace    VARCHAR(190),
    token_hash   VARCHAR(64)  NOT NULL,
    hint         VARCHAR(24)  NOT NULL,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_by   BIGINT,
    created_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_tokens_token_hash ON machine_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_machine_tokens_user_id ON machine_tokens (user_id);
CREATE INDEX IF NOT EXISTS idx_machine_tokens_cluster_id ON machine_tokens (cluster_id);
CREATE INDEX IF NOT EXISTS idx_machine_tokens_expires_at ON machine_tokens (expires_at);
