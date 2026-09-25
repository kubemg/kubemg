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

`Open` builds a standard `lib/pq` DSN from these and
connects through GORM's Postgres driver. Set `DB_SSLMODE=require` (or
`verify-full` if you're running a managed Postgres that supports it) against
anything that isn't a loopback or otherwise trusted private network — the
default of `disable` is a development convenience, not a production setting.

## What runs at boot: `AutoMigrate`

The schema is applied at boot by GORM's `AutoMigrate` over every model kubemg
defines:

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
- A file is written **from** what the boot migration actually does, never the
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
| The JWT signing key, when not supplied via `JWT_SECRET` | Postgres (`server_secrets`) — generated once at first boot and read on every subsequent boot, so it survives a restart without needing to be set explicitly. Encrypted under `KUBEMG_SECRET_KEY` when one is set |
| Just-in-time access requests and grants | Postgres (`jit_requests`, and `user_cluster_access` rows with `source='jit'`) |
| The alarm-watcher background-job lease | Postgres (`leases`) — see [Choosing a deployment](index.md#sizing-and-high-availability) |
| The TLS certificate kubemg mints for itself | **Disk**, under `/etc/kubemg/tls` (or wherever `KUBEMG_TLS_CERT_FILE`/`KEY_FILE` point) — never the database |

This split is why backing up the database alone is not a full backup: the
`tls-certs` volume (every already-installed agent has pinned that specific
certificate) and the recordings volume (audit evidence a database backup
alone cannot reconstruct) both need their own backup coverage. See
[Docker Compose](docker-compose.md#backup) and
[Choosing a deployment](index.md#what-the-management-plane-needs-regardless-of-where-it-runs).

## Credentials encrypted at rest

The database holds the credentials KubeMG has to present somewhere. With
`KUBEMG_SECRET_KEY` set, each one is stored encrypted (AES-256-GCM, a fresh
random nonce per value, written as `enc:v1:…`):

| Credential | Table.column |
|---|---|
| The generated session/kubeconfig signing key | `server_secrets.value` |
| Every agent's tunnel (registration) token | `clusters.agent_token` |
| Direct-mode ServiceAccount tokens | `clusters.service_account_token` |
| Observability datasource credentials | `observability_sources.credential` |
| Helm repository credentials | `helm_repositories.credential` |
| Alarm channel secrets (tokens, routing keys, passwords) | `alarm_channels.secret` |
| OIDC client secrets | `sso_providers.client_secret` |
| LDAP bind passwords | `sso_providers.ldap_bind_password` |

What is **not** encrypted, and why:

- Local user passwords and machine-account tokens are already stored only as
  hashes (bcrypt and SHA-256) — there is nothing to decrypt.
- Install download tickets and WebSocket tickets are stored as SHA-256 hashes.
- Each agent token also has a SHA-256 **lookup hash** beside it
  (`clusters.agent_token_hash`): a handshake finds the cluster by the hash,
  then compares the decrypted token. The token is encrypted rather than only
  hashed because the **Agent install** sheet re-renders the install package
  from it without rotating the agent's credential — that needs the value
  back.
- Cluster CA certificates, alarm channel headers, audit-forwarder CA bundles,
  the audit trail and everything else are not credentials and are stored as
  they are.

**The key is now as important as the database backup.** A restored database
is only usable with the key it was encrypted under:

- **Key unset** — the server boots, stores credentials in plaintext, and
  warns at boot. The setup wizard and the Deployment posture page say so too.
- **Key set on an existing install** — the next boot encrypts every
  plaintext value in place. It is safe to restart repeatedly: values already
  encrypted are left alone.
- **Key not exactly 32 bytes** — the server refuses to start.
- **Key changed, or removed, after values were encrypted** — the server
  refuses to start and names the first value it could not read. It never
  treats ciphertext as a credential and never falls back to plaintext.
  Restore the original key.
- **Key lost** — the encrypted credentials cannot be recovered. Recovery
  means clearing those columns and re-entering each credential: re-register
  direct-mode clusters, rotate every agent token and re-apply the install
  packages, re-enter datasource, Helm repository, alarm and SSO secrets. The
  generated signing key is replaced by a new one, which signs everyone out
  and invalidates every issued kubeconfig.

Keep the key in a secret manager or a sealed store that is backed up on its
own schedule — **never inside the database backup it protects**, where it
would protect nothing. `JWT_SECRET` from the environment still takes
precedence over the stored signing key, and is recommended for any
production install alongside `KUBEMG_SECRET_KEY`.

## Backup and restore

There's nothing kubemg-specific here beyond the split above — back up
Postgres the way you back up any Postgres database that matters:

- `pg_dump`/`pg_restore` (or your managed Postgres provider's snapshot
  mechanism) on a regular schedule.
- Restore into a database at the same major version (16) that the boot
  migration can then run against — a restore from an older schema is
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
- Back up `KUBEMG_SECRET_KEY` separately. A database restored without it does
  not boot — see [Credentials encrypted at rest](#credentials-encrypted-at-rest).

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
