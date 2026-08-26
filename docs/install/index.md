# Choosing a deployment

kubemg's management plane — console and gateway in one Go binary — ships as a
single container image, `ghcr.io/kubemg/kubemg`. There are two supported ways
to run it in production, plus the dev stack you should not run in production
at all.

| | Dev stack (`make up`) | [Docker Compose](docker-compose.md) | [Kubernetes](kubernetes.md) |
|---|---|---|---|
| Builds from source | Yes (bind-mounted) | No — pulls published images | No — pulls published images |
| Where it runs | Your laptop | A single VM or bare host | A cluster |
| TLS | Self-signed, on by default | Self-signed by default, or your own cert in `ssl/` | Terminate at the pod or at an ingress — see [TLS](tls.md) |
| CORS | Needed (Vite on a separate port) | Not needed (same-origin) | Not needed (same-origin) |
| Use it for | Evaluating kubemg, developing kubemg itself | A real install with no Kubernetes to run the management plane on | A real install alongside the clusters kubemg will manage |

If you don't already have a Kubernetes cluster to host the management plane
on, or you want the smallest possible number of moving parts, use
[Docker Compose](docker-compose.md) — `deploy/compose/` pulls three images
(`kubemg`, `postgres`, and the agent your *target* clusters pull) and builds
nothing, so it runs on a host with no toolchain. If you're already running
Kubernetes and want the management plane to live there too, use
[Kubernetes](kubernetes.md).

Either way, the cluster kubemg *manages* does not have to be the machine or
cluster the management plane runs on — the whole point of the bastion/agent
architecture is that a target cluster only ever dials **out** to the
management plane's public address.

## What the management plane needs, regardless of where it runs

- **PostgreSQL 16.** Users, grants, clusters, settings and the audit trail
  all live there. See [Database](database.md).
- **A public URL every target cluster can dial.** `KUBEMG_PUBLIC_URL` (or the
  Settings page override at runtime) is baked into every agent install
  command and every generated kubeconfig — it has to be the address a remote
  cluster reaches this process on, not its listen address or `localhost`.
- **TLS in front of it.** Not a hardening option: client-go refuses to send a
  bearer token over plain `http://`, so `kubectl exec` and every generated
  kubeconfig fail outright without it. See [TLS and certificates](tls.md) —
  it is the most detailed page in this section for a reason.
- **A persistent volume for session recordings**
  (`KUBEMG_SESSION_RECORDING_DIR`, default `/var/lib/kubemg/recordings`).
  Every `exec`/`attach` session is recorded for replay; an unmounted directory
  means recordings that vanish on the next restart, which is the audit
  evidence an auditor will ask for.
- **A persistent volume for the TLS material kubemg mints itself**
  (`/etc/kubemg/tls`), unless you supply your own certificate. That
  certificate is pinned into every already-installed agent's trust bundle —
  losing the volume means minting a new one, and every existing agent then
  fails its handshake against a certificate it does not recognize.

## Sizing and high availability

The management plane is close to stateless: every read and write goes through
PostgreSQL, and a session is a signed JWT rather than server-side state. That
means you can run more than one replica behind a load balancer for
availability, with two things to get right:

- **`JWT_SECRET`** is optional even with several replicas: left unset, each
  one mints a key on first boot and stores it via a conflict-safe upsert
  (`db.EnsureServerSecret`), so several replicas booting against the same
  database at once still converge on one shared key rather than each using
  its own. Set it explicitly only if you want a specific, known key you
  control the rotation of — see
  [Environment reference](environment.md#auth-jwt-bootstrap).
- **Exactly one replica polls for cluster-event alarms.** Watching for alarm
  conditions on attached clusters is the one piece of background work in
  kubemg whose cost scales with the number of replicas rather than with the
  number of callers — polling N times would put N times the read load on
  every target cluster's API server for no benefit. `pkg/db/lease.go`
  resolves this with a database-backed lease (`AcquireLease`, one row with an
  expiry, taken by one conditional `UPSERT`): every replica ticks, exactly one
  wins the lease and actually polls, and a killed replica's lease simply
  expires rather than needing to be released. This requires no
  configuration — it works the same whether you run one replica or ten.

The TLS certificate and the recordings directory are the two pieces of local
state; put both on shared/persistent volumes if you run more than one replica,
so every replica serves the same certificate and every recording is visible
regardless of which replica wrote it.

## Next

- [Docker Compose](docker-compose.md) — the single-host path
- [Kubernetes](kubernetes.md) — the in-cluster path
- [TLS and certificates](tls.md)
- [Environment reference](environment.md)
- [Production checklist](production-checklist.md)
