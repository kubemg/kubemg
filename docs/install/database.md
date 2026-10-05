# Database

kubemg needs **PostgreSQL 16**. This page says what is stored there, how the
schema is applied, and how to back it up and protect the credentials in it.

Every user, cluster, grant, group, setting, audit row and terminal-session
record lives in Postgres. Only session recordings (the `.cast.gz` files) and the
TLS material live on disk.

## Connecting

| Variable | Default | What it is |
|---|---|---|
| `DB_HOST` | `localhost` | Host. |
| `DB_PORT` | `5432` | Port. |
| `DB_USER` | `kubemg` | Role. |
| `DB_PASSWORD` | `kubemg_secret` | Password. Change this; it is a development placeholder. |
| `DB_NAME` | `kubemg` | Database name. |
| `DB_SSLMODE` | `disable` | libpq `sslmode`. |

Set `DB_SSLMODE=require` (or `verify-full` on a managed Postgres that supports
it) against anything but a loopback or trusted private network. `disable` is a
development convenience.

## Schema changes at boot

The schema is applied automatically on every boot, before the server accepts a
request. There is no migration command to run. It adds missing tables and
columns and never drops or renames anything; a few changes that need more than
that (such as widening a uniqueness constraint) run right after, in a fixed
order.

If a DBA must review or pre-apply schema changes under change control, the
repository's `backend/migrations/` directory holds idempotent reference SQL. The
server never runs it, and where it disagrees with what the server applies, the
server wins.

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
| The TLS certificate kubemg mints for itself | Postgres (`server_secrets`), with a working copy on disk under `/etc/kubemg/tls` that is written back from the database whenever it is missing — see [TLS](tls.md#the-minted-certificate-is-kept-in-the-database-too). Encrypted under `KUBEMG_SECRET_KEY` when one is set. A certificate you supply is never copied here |

Backing up the database alone is therefore not a full backup: the recordings
volume holds audit evidence the database cannot reconstruct. See
[Docker Compose](docker-compose.md#backup) and
[Choosing a deployment](index.md#what-the-management-plane-needs-regardless-of-where-it-runs).

## Credentials encrypted at rest

With `KUBEMG_SECRET_KEY` set, every credential kubemg has to present somewhere
is stored encrypted (AES-256-GCM, written as `enc:v1:…`):

| Credential | Table.column |
|---|---|
| The generated session/kubeconfig signing key, and the private key of the certificate kubemg minted for itself | `server_secrets.value` |
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
- Each agent token has a SHA-256 lookup hash beside it
  (`clusters.agent_token_hash`). The token itself is encrypted, not only hashed,
  because the **Agent install** sheet re-renders the package from it without
  rotating the credential.
- Cluster CA certificates, alarm channel headers, audit-forwarder CA bundles,
  the audit trail and everything else are not credentials and are stored as
  they are.

**The key is as important as the database backup.** A restored database is only
usable with the key it was encrypted under:

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

Back up Postgres as you would any database that matters (`pg_dump`/`pg_restore`
or your provider's snapshots), plus:

- **Same major version.** Restore into Postgres 16. Boot kubemg against the
  restored database and the schema is brought forward automatically.
- **The recordings volume**, on its own schedule. The minted TLS certificate is
  in the database backup.
- **`KUBEMG_SECRET_KEY`**, separately. A database restored without it does not
  boot; see [Credentials encrypted at rest](#credentials-encrypted-at-rest).

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
