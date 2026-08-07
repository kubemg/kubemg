-- 012 — the other consoles a cluster is operated from.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- A cluster already declares where its metrics and logs live; this says where
-- the Grafana that answers a question the fixed chart catalogue cannot is, and
-- where the Argo CD that owns half its workloads is. It stores an address and
-- nothing else: KubeMG holds no session for either tool, opens nothing, and the
-- operator signs in to them as themselves — which is why a URL carrying
-- userinfo is refused at the API rather than stored.

CREATE TABLE IF NOT EXISTS cluster_consoles (
    id         bigserial PRIMARY KEY,
    cluster_id bigint       NOT NULL,
    -- 'grafana' | 'argocd'
    kind       varchar(20)  NOT NULL,
    url        varchar(512) NOT NULL,
    -- The one identifier that opens the target on the right thing rather than on
    -- its front page. Optional; a console without one still gets a bare link.
    ref        varchar(190),
    created_at timestamptz,
    updated_at timestamptz
);

-- One row per cluster per kind: a cluster has *the* Grafana that answers for it,
-- not a list of candidates — the same rule observability_sources follows.
CREATE UNIQUE INDEX IF NOT EXISTS idx_console_cluster_kind
    ON cluster_consoles (cluster_id, kind);

-- Which datasource a query belongs to lives on the datasource row, because that
-- is the thing it identifies: one Grafana holds the metrics datasource and the
-- logs one, and they are two different uids. Without it an Explore deep link
-- would open on whatever that Grafana defaults to, which for a PromQL
-- expression is an error message rather than a chart.
ALTER TABLE observability_sources
    ADD COLUMN IF NOT EXISTS grafana_datasource varchar(190);
