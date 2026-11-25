-- 029 — team groups: a KubeMG group asserted to a cluster as kubemg:g:<name>.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.

-- The team group names an administrator confirmed the cluster's applied agent
-- package allows, comma-separated. Empty on every existing cluster, so nothing
-- new is asserted until a package is re-applied and the re-apply confirmed.
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS agent_team_groups text;

-- Team groups a call's grant named but KubeMG did not assert, because the
-- cluster's agent was not yet confirmed to allow them. Comma-separated.
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS withheld_team_groups text;
