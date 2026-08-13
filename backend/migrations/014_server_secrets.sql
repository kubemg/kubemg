-- 014 — the values the server generates for itself.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- KubeMG can now be installed without writing a configuration file first: the
-- console carries the operator through setup on first sign-in. That needs the
-- server to come up before anybody has told it anything, and the one value it
-- genuinely cannot start without is the key that signs sessions and generated
-- kubeconfigs. So it mints one on first boot and keeps it here.
--
-- This is a *secret*, and it is on its own table rather than in `settings` for
-- one reason: Store.Settings returns every row of that table and the result
-- feeds the admin settings API. A signing key living there would be one careless
-- field away from being serialised into a response. Nothing reads this table
-- except the boot path.
--
-- `JWT_SECRET` still wins wherever it is set. A deployment rotating it out of a
-- secret manager is unaffected, and nothing here overwrites what it supplied.
--
-- Losing a row means invalidating every session and every generated kubeconfig
-- at once — the same consequence as losing JWT_SECRET, and the reason this table
-- is part of the database backup rather than something to rebuild.

CREATE TABLE IF NOT EXISTS server_secrets (
    name       VARCHAR(64) PRIMARY KEY,
    value      TEXT                     NOT NULL,
    created_at TIMESTAMPTZ
);
