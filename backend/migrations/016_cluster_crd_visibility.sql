-- 016 — which of a cluster's custom resources the Explore sidebar offers.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- The sidebar's custom-resource sections are built from the cluster's own CRD
-- list, which is the only way to browse a cluster nobody here has heard of. The
-- cost is that a cluster running three operators declares a hundred kinds and
-- most of them are one operator talking to itself. So an administrator curates
-- the list per cluster, and this table is the result.
--
-- Rows are the *hidden* set: a CRD nobody has said anything about is shown,
-- which is what every install already does, and turning one back on is deleting
-- a row rather than accumulating a record of everything a cluster ever served.
--
-- This is curation, not access control. Hiding a kind removes it from the
-- navigation and from nothing else — what may be read is the cluster's own RBAC
-- to decide, and the object routes still address a hidden kind exactly as
-- kubectl would.

CREATE TABLE IF NOT EXISTS cluster_crd_visibility (
    id         bigserial PRIMARY KEY,
    cluster_id bigint       NOT NULL,
    -- 'plural.group', how kubectl names a resource unambiguously —
    -- e.g. 'virtualservices.networking.istio.io'.
    resource   varchar(253) NOT NULL,
    -- The administrator who last wrote the row. The audit trail carries the act;
    -- this carries it where the row is read.
    hidden_by  bigint,
    created_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_crd_visibility_cluster_resource
    ON cluster_crd_visibility (cluster_id, resource);
