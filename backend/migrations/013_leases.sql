-- 013 — which replica does a piece of unattended work.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- Almost everything KubeMG does happens because somebody asked for it, so
-- replicas behind a load balancer share the work by definition. Background work
-- is the exception: a poller started in every process polls once per process,
-- and the cost of that lands on the *target cluster* rather than on KubeMG —
-- which is the wrong place for it, since running KubeMG in three replicas for
-- availability is not a request to triple the read load on a production API
-- server.
--
-- One row per background job, holding the process that currently owns it and
-- when that claim goes stale. It is an expiring lease rather than a lock because
-- a replica that is killed cannot release anything, and a held lock nobody can
-- release is an outage that needs a DBA; an expiry means the worst case is one
-- idle window. Acquisition is a single conditional upsert — see
-- pkg/db/lease.go — so two replicas ticking at the same instant is resolved by
-- the database and exactly one of them sees a row affected.
--
-- The row is operational state, not history: it is safe to truncate, and a
-- server that finds the table empty simply takes the lease on its next tick.

CREATE TABLE IF NOT EXISTS leases (
    name       VARCHAR(64) PRIMARY KEY,
    holder     VARCHAR(64)              NOT NULL,
    expires_at TIMESTAMPTZ              NOT NULL,
    updated_at TIMESTAMPTZ
);
