# Database

kubemg needs **PostgreSQL 16**. Every user, cluster, grant, group, setting,
audit row and terminal-session record lives there — nothing is stored
anywhere else except session recordings themselves (the `.cast.gz` files) and
the TLS material on disk.

## Connecting

| Variable | Default | What it is |
|---|---|---|
| `DB_HOST` | `localhost` | Host. |
| `DB_PORT` | `5432` | Port. |
| `DB_USER` | `kubemg` | Role. |
| `DB_PASSWORD` | `kubemg_secret` | Password — change this; it is a development placeholder. |
| `DB_NAME` | `kubemg` | Database name. |
| `DB_SSLMODE` | `disable` | libpq `sslmode`. |

`Open` (`backend/pkg/db/db.go`) builds a standard `lib/pq` DSN from these and
connects through GORM's Postgres driver. Set `DB_SSLMODE=require` (or
`verify-full` if you're running a managed Postgres that supports it) against
anything that isn't a loopback or otherwise trusted private network — the
default of `disable` is a development convenience, not a production setting.

## What runs at boot: `AutoMigrate`

The schema is applied by `db.Migrate`, which is a plain
`gdb.AutoMigrate(...)` call over every model kubemg defines:

```
User, Cluster, UserClusterAccess, Group, UserGroup, GroupClusterAccess,
AuditEvent, TerminalSession, MachineToken, Setting, ServerSecret,
ObservabilitySource, ClusterConsole, SSOProviderConfig, SSOGroupMapping,
AlarmChannel, AlarmRule, GuardrailPolicy, JitRequest, Lease,
PostureAcknowledgement, ClusterCRDVisibility
```

This runs automatically, every boot, before the server accepts a request —
there is no separate migration command to run and no migration state to
track beyond what GORM's own `AutoMigrate` does (add missing tables and
columns; it never drops or renames anything). A couple of migrations need
more than a column add and are handled by small hand-written Go functions
that run immediately after `AutoMigrate`, in a fixed order — for example
widening the uniqueness constraint on `user_cluster_access` from
`(user_id, cluster_id)` to `(user_id, cluster_id, source)` to support
just-in-time grants existing alongside standing ones.

## `backend/migrations/*.sql`: reference DDL, executed by nothing

**Nothing in that directory runs.** The files exist because the schema is a
deployment artefact for someone who is not running the binary: on an on-prem
install, the database is frequently owned by a DBA who will not read Go
struct tags, needs to review what an upgrade does to a table they're
responsible for, and may want to pre-apply a change under change control
before the new image starts.

Two rules keep them trustworthy:

- Every statement is **idempotent** (`IF NOT EXISTS`/`IF EXISTS`), because
  `AutoMigrate` may already have applied it by the time anyone runs the file
  by hand — running it again must be a no-op, never an error.
- A file is written **from** what `db.Migrate` actually does, never the
  other way around. If a numbered file and the Go code ever disagree, the Go
  code is what ran, and the file is a bug to fix — not a spec to make the
  code match.

If you're on a database with a DBA in the loop, hand them this directory: pre-
applying `011_jit_access.sql` through `016_cluster_crd_visibility.sql` (and
any that follow) under whatever change-control process your organization
already uses is exactly what it's for. `AutoMigrate` then finds the columns
and tables already present and leaves them alone.

## What data lives where

| Data | Where |
|---|---|
| Users, groups, memberships, cluster grants | Postgres (`users`, `groups`, `user_groups`, `user_cluster_access`, `group_cluster_access`) |
| Clusters, connection mode, registration tokens | Postgres (`clusters`) |
| The audit trail — every proxied call, refusals included | Postgres (`audit_events`) |
| Session recording **metadata** — who, which cluster, duration, truncated, encrypted | Postgres (`terminal_sessions`) |
| Session recording **content** — the actual `.cast.gz` bytes | **Disk**, under `KUBEMG_SESSION_RECORDING_DIR` — not the database. A row with no corresponding file, or a file with no row, is treated as an orphan and cleaned up by retention. |
| Settings (public URL, agent image, audit retention, etc.) | Postgres (`settings`, key/value) |
| The JWT signing key, when not supplied via `JWT_SECRET` | Postgres (`server_secrets`) — generated once at first boot and read on every subsequent boot, so it survives a restart without needing to be set explicitly |
| Just-in-time access requests and grants | Postgres (`jit_requests`, and `user_cluster_access` rows with `source='jit'`) |
| The alarm-watcher background-job lease | Postgres (`leases`) — see [Choosing a deployment](index.md#sizing-and-high-availability) |
| The TLS certificate kubemg mints for itself | **Disk**, under `/etc/kubemg/tls` (or wherever `KUBEMG_TLS_CERT_FILE`/`KEY_FILE` point) — never the database |

This split is why backing up the database alone is not a full backup: the
`tls-certs` volume (every already-installed agent has pinned that specific
certificate) and the recordings volume (audit evidence a database backup
alone cannot reconstruct) both need their own backup coverage. See
[Docker Compose](docker-compose.md#backup) and
[Choosing a deployment](index.md#what-the-management-plane-needs-regardless-of-where-it-runs).

## Backup and restore

There's nothing kubemg-specific here beyond the split above — back up
Postgres the way you back up any Postgres database that matters:

- `pg_dump`/`pg_restore` (or your managed Postgres provider's snapshot
  mechanism) on a regular schedule.
- Restore into a database at the same major version (16) that `db.Migrate`
  can then run against on the next boot — a restore from an older schema is
  exactly the case `AutoMigrate` and the reference DDL exist to make safe:
  bring the restored database up, boot kubemg against it, and `AutoMigrate`
  brings the schema forward to whatever this build expects.
- If your install predates a given migration and you'd rather review the DDL
  before the server starts and applies it, pre-apply the relevant
  `backend/migrations/*.sql` files under your own change control first —
  they're written to be safe to run either before or after `AutoMigrate`
  does the same work.
- Back up the `tls-certs` and session-recordings volumes on their own
  schedule alongside the database — see the table above for why a database
  backup alone is incomplete.

## Managed PostgreSQL

A managed Postgres (RDS, Cloud SQL, Azure Database for PostgreSQL, etc.) at
version 16 works with no changes beyond pointing `DB_HOST`/`DB_PORT` at it
and setting `DB_SSLMODE=require` (or the stricter mode your provider
recommends). This is the recommended production posture — see
[Production checklist](production-checklist.md).

## Next

- [Environment reference](environment.md)
- [Production checklist](production-checklist.md)
- [Upgrading](upgrading.md)
