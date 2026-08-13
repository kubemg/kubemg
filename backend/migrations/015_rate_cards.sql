-- 015 — what an hour of capacity costs, according to whoever runs this install.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- KubeMG holds no cloud credential and calls no billing API, so the rates are
-- typed in rather than discovered, and everything computed from them is an
-- estimate that says so. Nothing in this table is a secret: a published list
-- price is public, and an operator's own negotiated rate is commercially
-- sensitive but not a credential — it opens nothing.

CREATE TABLE IF NOT EXISTS rate_cards (
    id                  bigserial PRIMARY KEY,
    -- The cluster these rates price, or 0 for the installation-wide default.
    -- Zero is a sentinel rather than NULL because a unique index does not
    -- constrain the rows where the column is null: two installation defaults
    -- would be storable, and which one applied would be whichever the query
    -- happened to order first.
    cluster_id          bigint           NOT NULL,
    -- 'aws' | 'gcp' | 'azure' | 'custom'. Descriptive only — nothing in KubeMG
    -- behaves differently per provider; the field records which price list the
    -- numbers were copied from.
    provider            varchar(20)      NOT NULL,
    -- ISO 4217, echoed and never converted. KubeMG has no exchange rate and
    -- will not invent one.
    currency            varchar(3)       NOT NULL,
    cpu_core_hour       double precision NOT NULL DEFAULT 0,
    memory_gib_hour     double precision NOT NULL DEFAULT 0,
    storage_gib_month   double precision NOT NULL DEFAULT 0,
    load_balancer_month double precision NOT NULL DEFAULT 0,
    -- Where the operator records what these rates actually are: the instance
    -- family, the region, the discount they already reflect. Shown wherever the
    -- figures are, because an estimate whose provenance is off screen is one
    -- nobody can argue with.
    note                varchar(500),
    created_at          timestamptz,
    updated_at          timestamptz
);

-- One card per scope. A cluster has *the* rates it is priced at, not a list of
-- candidates — the same rule cluster_consoles and observability_sources follow.
CREATE UNIQUE INDEX IF NOT EXISTS idx_rate_card_scope
    ON rate_cards (cluster_id);
