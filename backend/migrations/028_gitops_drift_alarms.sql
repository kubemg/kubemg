-- 028 — alarms on GitOps drift.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.

-- A gitops_drift rule's matchers, comma-separated like the others.
ALTER TABLE alarm_rules ADD COLUMN IF NOT EXISTS gitops_tools text;
ALTER TABLE alarm_rules ADD COLUMN IF NOT EXISTS gitops_apps text;
