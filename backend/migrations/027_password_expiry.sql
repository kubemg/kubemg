-- 027 — password expiry, and the mail relay its reminders go through.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.

ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_expired_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_notice_due timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_notice_days bigint NOT NULL DEFAULT 0;

-- A password set before the policy existed counts from the upgrade, not from
-- the account's creation (db.Migrate does this at every boot; it is idempotent).
UPDATE users SET password_changed_at = now()
 WHERE password_changed_at IS NULL AND password_hash <> '';

CREATE TABLE IF NOT EXISTS smtp_configs (
    id           bigserial PRIMARY KEY,
    host         varchar(255) NOT NULL,
    port         bigint NOT NULL,
    security     varchar(16) NOT NULL DEFAULT 'starttls',
    username     varchar(255),
    password     text,          -- sealed (enc:v1:) when KUBEMG_SECRET_KEY is set
    from_address varchar(320) NOT NULL,
    updated_at   timestamptz,
    updated_by   bigint
);
