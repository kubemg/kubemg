-- 011: just-in-time elevated access.
--
-- Reference DDL for what db.Migrate applies at boot; see README.md in this
-- directory — nothing here is executed by the server, and every statement is
-- idempotent so pre-applying it is safe.
--
-- Two changes, and the second is the one to read carefully.

-- 1. The request table. The primary key is a UUID string rather than a sequence
--    because a request id travels: into a chat message, into a signed approval
--    callback and into the audit trail, and a guessable id in any of those is an
--    invitation to try the next number along.
CREATE TABLE IF NOT EXISTS jit_requests (
    id                 varchar(36) PRIMARY KEY,
    requester_id       bigint      NOT NULL,
    -- Denormalised on purpose: a request is the record of a decision and has to
    -- read correctly after the account or the cluster it names is gone.
    requester_username varchar(120),
    cluster_id         bigint      NOT NULL,
    cluster_name       varchar(120),
    requested_role     varchar(60) NOT NULL,
    namespaces         text,
    duration_minutes   bigint      NOT NULL,
    -- Mandatory, and the field that makes the whole workflow worth having.
    reason             text        NOT NULL,
    status             varchar(16) NOT NULL,
    approver_id        bigint,
    approver_username  varchar(120),
    approver_comment   text,
    approved_at        timestamptz,
    -- Set at approval, not at creation: the window an approver grants starts when
    -- they grant it, not when somebody asked.
    expires_at         timestamptz,
    created_at         timestamptz,
    updated_at         timestamptz
);

CREATE INDEX IF NOT EXISTS idx_jit_requests_requester_id ON jit_requests (requester_id);
CREATE INDEX IF NOT EXISTS idx_jit_requests_cluster_id   ON jit_requests (cluster_id);
CREATE INDEX IF NOT EXISTS idx_jit_requests_status       ON jit_requests (status);
CREATE INDEX IF NOT EXISTS idx_jit_requests_expires_at   ON jit_requests (expires_at);

-- 2. Grants can now end, and a user can hold more than one per cluster.
--
--    An elevation is a *second row* rather than an edit of the standing grant. That
--    is what lets it expire with no restore step and with no window in which the
--    requester has lost the access they permanently hold — the row it outranks was
--    never touched. It also means the old uniqueness, one grant per (user,
--    cluster), is wrong: three provenances are three different facts about the same
--    person, and only two rows of the *same* provenance are two answers to one
--    question.
--
--    Order matters. The wider index is created before the narrower one is dropped,
--    so the table is never without a uniqueness constraint.
ALTER TABLE user_cluster_access
    ADD COLUMN IF NOT EXISTS expires_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_user_cluster_access_expires_at
    ON user_cluster_access (expires_at);

-- Rows predating provenance are local by definition: an administrator wrote them.
UPDATE user_cluster_access SET source = 'local' WHERE source IS NULL OR source = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_cluster_source
    ON user_cluster_access (user_id, cluster_id, source);

DROP INDEX IF EXISTS idx_user_cluster;
