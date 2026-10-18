-- 025 — who refused a refused call.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- The trail records every attempt, including the ones KubeMG refuses before a
-- cluster is asked: a namespace outside the grant, a cluster-wide list for a
-- scoped grant, a kind KubeMG will not author, a guardrail. `refused_by` is
-- 'kubemg' on those rows and empty everywhere else, so "KubeMG said no" and
-- "the cluster said no" are one filter apart rather than a reading of prose.
--
-- Empty on every row written before this migration, which is "not marked", not
-- "the cluster refused": the proxy's own namespace and guardrail refusals were
-- recorded before the column existed and carry nothing here.

ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS refused_by VARCHAR(16);
