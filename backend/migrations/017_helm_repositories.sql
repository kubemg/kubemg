-- 017 — where charts may be installed from, and what those repositories hold.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- A repository is **server-wide rather than per-cluster**, which is the one
-- thing about this table worth explaining to somebody reading only the schema.
-- Every other configuration table here is keyed by cluster — observability
-- sources, consoles, CRD visibility — and this one deliberately is not: what
-- this installation may reach out to and download executable templates from is
-- a fact about the installation, and duplicating it per cluster would mean an
-- operator adding their internal mirror once per cluster and a fleet where half
-- the clusters can install cert-manager.
--
-- `credential` is stored and never serialized. The API reports whether one is
-- set, never what it is, and an edit that omits the field keeps the stored one
-- rather than clearing it — the same rule observability_sources follows.
--
-- The starter catalogue is seeded into this table on first boot and guarded by a
-- marker in `settings` (`helm_repositories_seeded`), so a repository an operator
-- deliberately removed does not come back on the next restart. Every seeded row
-- is editable and deletable like one an operator typed; `seeded` only records
-- where it came from.

CREATE TABLE IF NOT EXISTS helm_repositories (
    id             bigserial PRIMARY KEY,
    -- The identity. It is what a release records as its source and what an
    -- install names, so it is unique across the installation.
    name           varchar(63)  NOT NULL,
    url            text         NOT NULL,
    username       varchar(255),
    -- Never leaves the process. See the note above.
    credential     text,
    description    text,
    seeded         boolean      NOT NULL DEFAULT false,
    -- The last sync's verdict and its reason. Stored rather than derived: a
    -- repository that cannot be reached keeps serving what it last held, and an
    -- operator has to be able to see why what they are looking at is three days
    -- old. 'pending' | 'ok' | 'error'.
    status         varchar(20)  NOT NULL DEFAULT 'pending',
    status_message text,
    chart_count    integer      NOT NULL DEFAULT 0,
    synced_at      timestamptz,
    created_at     timestamptz,
    updated_at     timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_helm_repositories_name
    ON helm_repositories (name);

-- One chart of one repository, as the last successful sync left it.
--
-- `versions` is a JSON array rather than a third table, and that is a deliberate
-- narrowing: a version is never queried independently of its chart — every read
-- is "the versions of this chart", for a dropdown — and the set is bounded to
-- the newest few before it is written, so the row cannot grow. A versions table
-- would buy the ability to ask a question nothing asks, at the cost of a join on
-- every catalogue read and a second delete on every sync.
--
-- A sync **replaces** a repository's rows rather than merging them, in one
-- transaction: the catalogue *is* the index, so a chart the repository stopped
-- publishing has to disappear, and the transaction is what keeps a reader from
-- seeing an empty catalogue mid-sync.
CREATE TABLE IF NOT EXISTS helm_charts (
    id            bigserial PRIMARY KEY,
    repository_id bigint       NOT NULL,
    name          varchar(255) NOT NULL,
    description   text,
    icon          text,
    home          text,
    deprecated    boolean      NOT NULL DEFAULT false,
    -- JSON array of {version, app_version, created, digest, deprecated, urls},
    -- newest first.
    versions      text,
    updated_at    timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_helm_chart_repo_name
    ON helm_charts (repository_id, name);
