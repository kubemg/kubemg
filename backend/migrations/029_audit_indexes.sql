-- 029 — audit indexes for the reads the server runs.
--
-- Reference DDL. The schema is applied by db.Migrate (AutoMigrate); this file
-- exists because on an on-prem install the database is often owned by a DBA who
-- will not read struct tags and may pre-apply a change under change control.
-- Every statement is idempotent. If this and the Go code disagree, the Go code
-- is what ran.
--
-- audit_events carried eleven single-column indexes and no composite one, and
-- every batched insert maintains all of them. Measured on PostgreSQL 16 against
-- 1.2M rows over 30 days, inserted in time order:
--
--   * A non-admin's trail and a cluster's trail are read newest first. Planned
--     on idx_audit_at, the server walks every account's rows backwards and
--     filters: for an account or a cluster idle for the last 20 days, the first
--     page took 66 ms and grows with the table. On (user_id, at, id) and
--     (cluster_id, at, id) it is read off the index in order: 0.7 ms. Counting
--     a day of one account's rows went from 4.2 ms to 0.14 ms.
--   * The GitOps attribution (the last write to one object) combined the
--     cluster_id, namespace and verb indexes in a bitmap over ~85k rows: 15 ms.
--     On name it reads the object's own rows: 0.3 ms. A composite
--     (cluster_id, resource, name, at) did no better on this read and slowed
--     inserts by about 17%.
--   * No plan used streaming, guardrail_policy or source_addr. user_id and
--     cluster_id alone are the leading columns of the composites above.
--   * status stays: a status filter over the whole trail counts in 5 ms with
--     it and 99 ms without. verb and namespace stay for the same reason.
--
-- Inserts, in batches of 100 as the server writes them, took about as long
-- with the new set as with the old. The indexes take more space: the
-- composites carry a timestamp per row, which B-tree deduplication cannot fold
-- the way it folds a column of repeated user ids.
--
-- On a large trail these take a while to build, and CREATE INDEX holds writes
-- to the table meanwhile. Run them with CONCURRENTLY, outside a transaction,
-- to pre-apply without that.

CREATE INDEX IF NOT EXISTS idx_audit_user_at ON audit_events (user_id, at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_cluster_at ON audit_events (cluster_id, at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_name ON audit_events (name);

DROP INDEX IF EXISTS idx_audit_events_user_id;
DROP INDEX IF EXISTS idx_audit_events_cluster_id;
DROP INDEX IF EXISTS idx_audit_events_streaming;
DROP INDEX IF EXISTS idx_audit_events_guardrail_policy;
DROP INDEX IF EXISTS idx_audit_events_source_addr;
