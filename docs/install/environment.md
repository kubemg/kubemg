# Environment reference

Every variable the management plane reads at boot, from
`backend/pkg/config/config.go`. All of them are optional — the process starts
with defaults if none are set, though several of those defaults are wrong for
a real install (see [Production checklist](production-checklist.md)).

!!! tip "Boot-time default vs. runtime setting"
    Four of these — marked below — are only the **boot-time default**. Once
    the server is running, an administrator can override them from the
    **Settings** page without a restart; the environment variable only
    decides what an install starts with before anyone has configured
    anything. See [Settings reference](../reference/settings.md) for the
    runtime side of that split.

## Parse rules

These matter because a typo in an environment variable never fails the boot —
it silently falls back to the default instead:

- **Booleans** (`envBool`): anything `strconv.ParseBool` doesn't accept (so,
  anything other than `1`/`t`/`T`/`TRUE`/`true`/`True`/`0`/`f`/`F`/`FALSE`/
  `false`/`False`) falls back to the default rather than failing the boot.
- **Integers** (`envInt`): must parse as a positive integer; anything else,
  including zero or negative, falls back to the default.
- **Durations** (`envDuration`): accepts a Go duration string (`30s`, `5m`,
  `12h`) **or** a bare integer, which is interpreted as a number of seconds.
  Anything else falls back to the default.
- **Lists** (`envList`): comma-separated; each entry is trimmed, empty
  entries are dropped, and if nothing is left after that the whole variable
  falls back to the default.

## Core / server

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_LISTEN_ADDR` | `:8080` (dev) / `:8443` (production image) | Listen address. Must be loopback if `KUBEMG_TLS_ENABLED=false` unless `KUBEMG_ALLOW_INSECURE=true` — see [TLS](tls.md). |

## Database

| Variable | Default | What it is |
|---|---|---|
| `DB_HOST` | `localhost` | PostgreSQL host. |
| `DB_PORT` | `5432` | PostgreSQL port. |
| `DB_USER` | `kubemg` | PostgreSQL user. |
| `DB_PASSWORD` | `kubemg_secret` | PostgreSQL password. **Change this in production** — the default is a development placeholder. |
| `DB_NAME` | `kubemg` | Database name. |
| `DB_SSLMODE` | `disable` | libpq `sslmode`. Use `require` (or stricter) against anything but a loopback/private-network Postgres. |

See [Database](database.md) for AutoMigrate behavior and the reference DDL.

## Auth / JWT / bootstrap

| Variable | Default | What it is |
|---|---|---|
| `JWT_SECRET` | generated, kept in the database | Signs sessions, generated kubeconfigs and JIT approval callback tokens. Unset, the server mints a 32-byte random key on first boot and stores it (`db.EnsureServerSecret`, an `ON CONFLICT DO NOTHING` insert followed by a read-back, so several replicas booting at once still converge on the same key rather than racing). Set it explicitly to supply your own key, or to be able to rotate it deliberately — which invalidates every issued token at once. |
| `JWT_TTL` | `12h` | Session token lifetime. |
| `KUBEMG_ADMIN_USERNAME` | `admin` | Bootstrap administrator's username, created only when the users table is empty. |
| `KUBEMG_ADMIN_PASSWORD` | generated, printed once to the log | Bootstrap administrator's password. Left unset, a random 20-character password (drawn from an alphabet with no visually-ambiguous characters) is generated and logged exactly once. Setup will not let you finish until it's changed either way. |
| `KUBEMG_SA_NAMESPACE` | `kubemg-system` | The namespace on a **direct-mode** target cluster that holds the per-user service accounts kubemg's TokenRequest calls create. Irrelevant to agent-mode clusters. |

## Public URL & agent

*`KUBEMG_PUBLIC_URL`, `KUBEMG_AGENT_IMAGE` and `KUBEMG_AGENT_NAMESPACE` are
boot-time defaults only — each is overridable at runtime from Settings.*

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_PUBLIC_URL` | `http://localhost:8080` | The address agents and operators reach this server on. Baked into every generated agent install command and kubeconfig — must be reachable from a **target cluster**, not this process's own listen address. A non-HTTPS value here surfaces as a warning rather than a kubeconfig that silently fails at first use. |
| `KUBEMG_AGENT_IMAGE` | pinned release image (`ghcr.io/kubemg/kubemg-agent:<version>`) | The agent container image rendered into every generated install manifest. Point this at an internal mirror for an air-gapped install. |
| `KUBEMG_AGENT_NAMESPACE` | `kubemg-system` | Namespace the agent is installed into on target clusters. |

## TLS

See [TLS and certificates](tls.md) for the full detail — this is the summary.

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_TLS_ENABLED` | `false` | Terminate HTTPS in this process. |
| `KUBEMG_TLS_CERT_FILE` / `KUBEMG_TLS_KEY_FILE` | `/etc/kubemg/tls/tls.crt` / `tls.key` | Where a minted (or explicitly configured) pair lives. |
| `KUBEMG_TLS_SUPPLIED_DIR` | `/etc/kubemg/ssl` | Checked first; a recognised pair here wins over anything minted. |
| `KUBEMG_TLS_SELF_SIGNED` | `true` | Mint a self-signed pair when nothing is supplied. `false` refuses to start instead of minting. |
| `KUBEMG_TLS_HOSTS` | — | Extra SANs for a minted certificate, comma-separated. |
| `KUBEMG_AGENT_CA_BUNDLE` | — | The CA chain agents must trust, when it isn't one this process's own certificate can reveal (an ingress, an internal PKI). Validated at boot. |
| `KUBEMG_ALLOW_INSECURE` | `false` | Serve plaintext HTTP on a non-loopback address anyway. Only correct behind a TLS-terminating proxy. |

## Session recording

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_SESSION_RECORDING_ENABLED` | `true` | Record every `exec`/`attach` for replay. |
| `KUBEMG_SESSION_RECORDING_DIR` | `/var/lib/kubemg/recordings` | Where `.cast.gz` recordings are written. **Mount this on a persistent volume.** |
| `KUBEMG_SESSION_RECORDING_MAX_BYTES` | recorder's own default (32 MiB) | Per-recording cap; `0` (or unset) takes the built-in default. |
| `KUBEMG_SESSION_RECORDING_KEY` | — | 32 bytes, hex or base64 (`openssl rand -base64 32`). Encrypts recordings at rest. Unset, recordings are written in plaintext and the server warns loudly at boot — this is the documented default every existing install starts in, so the warning is informational rather than an error. **Keep the key out of the backup that holds the recordings volume** — a key stored beside the ciphertext it protects defends against nothing, and losing it loses the recordings. |
| `KUBEMG_SESSION_RECORDING_INPUT` | `true` | Record keystrokes as well as output. `false` keeps only what the container printed — set this where operators type credentials into interactive tools, since that's exactly what a prompt refuses to echo and therefore what dropping input actually loses. |

## Caching

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_RESOURCE_CACHE_TTL` | `5s` | How long a per-caller cluster read is served from memory before being re-asked of the cluster. A **negative** value turns the cache off entirely. |
| `KUBEMG_EVENT_CACHE_TTL` | `30s` | The same window, specifically for the cluster events timeline — longer by default because Events are the cluster's own append-only record and nothing kubemg does writes one, so there's no write for a stale cache entry to hide. |
| `KUBEMG_EVENT_SCAN_LIMIT` | `4000` | How many events a single timeline read walks before reporting the answer as partial — bounds the cost of "read the newest events" on a cluster holding tens of thousands of them. |

## Audit retention

*Boot-time default only — overridable at runtime from Settings, where it is
also validated on the way in (1–3650 days) and on the way out.*

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_AUDIT_RETENTION_DAYS` | `30` | How long proxied calls (and, by default, session recordings) are kept before the background pruner drops them. |

## CORS

| Variable | Default | What it is |
|---|---|---|
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173,http://127.0.0.1:5173` | Browser origins allowed to call the API, comma-separated. **Not needed at all** in the production single-image deployment, where the console is served same-origin — this only matters for the dev stack, where Vite serves the console on a different port than the API. |

## Copy-pasteable annotated `.env`

For the Docker Compose deployment at `deploy/compose/` (see
[Docker Compose](docker-compose.md)):

```dotenv
# --- credentials --------------------------------------------------------
DB_PASSWORD=CHANGE_ME                         # openssl rand -base64 24
JWT_SECRET=CHANGE_ME                          # openssl rand -base64 48 — required if >1 replica
KUBEMG_ADMIN_USERNAME=admin
KUBEMG_ADMIN_PASSWORD=                        # leave empty: generated + logged once on first boot

# --- reachability --------------------------------------------------------
# The address TARGET CLUSTERS dial. Never localhost past a single-host demo.
KUBEMG_PUBLIC_URL=https://kubemg.internal:8443
KUBEMG_TLS_HOSTS=kubemg.internal,192.0.2.10   # every other name/IP that dials in

# --- TLS ------------------------------------------------------------------
# Leave both at their defaults if you're dropping a real cert into ssl/.
KUBEMG_TLS_SELF_SIGNED=true
KUBEMG_TLS_SUPPLIED_DIR=/etc/kubemg/ssl
# Only if TLS is terminated by a proxy in front of kubemg:
# KUBEMG_TLS_ENABLED=false
# KUBEMG_ALLOW_INSECURE=true
# KUBEMG_AGENT_CA_BUNDLE=/etc/kubemg/ssl/proxy-ca.crt

# --- air-gapped / internal registry --------------------------------------
# KUBEMG_IMAGE=registry.internal/kubemg/kubemg:0.7.1
# KUBEMG_POSTGRES_IMAGE=registry.internal/postgres:16-alpine
# KUBEMG_AGENT_IMAGE=registry.internal/kubemg/kubemg-agent:0.7.1
KUBEMG_AGENT_NAMESPACE=kubemg-system

# --- audit & recordings ----------------------------------------------------
KUBEMG_AUDIT_RETENTION_DAYS=30                # also editable at runtime from Settings
KUBEMG_SESSION_RECORDING_ENABLED=true
KUBEMG_SESSION_RECORDING_KEY=CHANGE_ME        # openssl rand -base64 32 — keep out of the recordings backup
KUBEMG_SESSION_RECORDING_INPUT=true

# --- database ---------------------------------------------------------------
DB_SSLMODE=require                            # against anything but a loopback/private Postgres

# --- caching (defaults are almost always fine) ------------------------------
# KUBEMG_RESOURCE_CACHE_TTL=5s
# KUBEMG_EVENT_CACHE_TTL=30s
```

## Next

- [Settings reference](../reference/settings.md) — the runtime side of the
  boot-time defaults above
- [TLS and certificates](tls.md)
- [Database](database.md)
- [Production checklist](production-checklist.md)
