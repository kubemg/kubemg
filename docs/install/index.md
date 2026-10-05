# Choosing a deployment

kubemg's management plane (console and gateway in one binary) ships as one
image, `ghcr.io/kubemg/kubemg`. Pick Docker Compose or Helm below; the dev stack
is for evaluation only.

| | Dev stack (`make up`) | [Docker Compose](docker-compose.md) | [Kubernetes (Helm)](kubernetes.md) |
|---|---|---|---|
| Builds from source | Yes | No, pulls published images | No, pulls published images |
| Runs on | Your laptop | A single VM or bare host | A cluster |
| TLS | Self-signed, on by default | Self-signed, or your cert in `ssl/` | At the pod or at an ingress, see [TLS](tls.md) |
| CORS | Needed (Vite on another port) | Not needed | Not needed |
| Use it for | Evaluating or developing kubemg | A real install with no Kubernetes to host it | A real install alongside the clusters it manages |

No Kubernetes to host it, or want the fewest moving parts: use
[Docker Compose](docker-compose.md). Already running Kubernetes: use the
[Helm chart](kubernetes.md).

The clusters kubemg manages need not be where it runs. A target cluster only
ever dials **out** to the management plane's public address.

## What the management plane needs, regardless of where it runs

- **PostgreSQL 16.** Users, grants, clusters, settings and the audit trail
  live there. See [Database](database.md).
- **A public URL every target cluster can dial.** `KUBEMG_PUBLIC_URL` (or the
  Settings override) goes into every agent install command and kubeconfig. It
  must be the address a remote cluster reaches, not a listen address or
  `localhost`.
- **TLS in front of it.** Not optional: `kubectl` will not send a bearer token
  over plain `http://`, so `kubectl exec` and every generated kubeconfig fail
  without it. See [TLS and certificates](tls.md).
- **A persistent volume for session recordings**
  (`KUBEMG_SESSION_RECORDING_DIR`, default `/var/lib/kubemg/recordings`).
  Unmounted, recordings vanish on restart, and they are the evidence an
  auditor asks for.
- **The database, backed up.** Unless you supply a certificate, kubemg mints
  one on first boot, pins it into every agent package, and keeps it in the
  database as well as on disk. Losing the database loses it, plus every
  agent's registration token. See
  [TLS](tls.md#the-minted-certificate-is-kept-in-the-database-too).

## Sizing and high availability

**Run exactly one kubemg replica.** An agent's tunnel is held in the memory of
the replica its connection reached. With two replicas, every request that
lands on the other one (Explore, terminal, browser shell, `kubectl` through an
issued kubeconfig) answers `503 no agent tunnel is attached to this cluster`,
and the fleet page shows the agent as not attached. It looks like the cluster
is down. Session affinity does not help, because the tunnel's replica is
decided by where the agent connected, not where the user did.

Availability comes from restarting quickly. After a restart every agent
reconnects on its own within a minute (its retry backs off to 60 seconds). On
Kubernetes use `strategy: Recreate`, see [Kubernetes](kubernetes.md#replicas).

??? info "What already works with more than one replica"
    The rest of the management plane is close to stateless: reads and writes go
    through PostgreSQL and a session is a signed JWT.

    - **`JWT_SECRET`** is optional. Left unset, each replica mints a key on
      first boot and stores it with a conflict-safe upsert, so replicas booting
      together converge on one key. Set it only if you want a known key whose
      rotation you control, see
      [Environment reference](environment.md#auth-jwt-bootstrap).
    - **Exactly one replica polls for cluster-event alarms.** Replicas compete
      for a database lease (one row with an expiry); the winner polls, and a
      killed replica's lease simply expires. No configuration needed.

The recordings directory is the only local state; put it on a persistent
volume. The pinned certificate is not local state: every boot writes it back
from the database.

## Next

- [Docker Compose](docker-compose.md) for the single-host path
- [Kubernetes](kubernetes.md) for the Helm chart
- [TLS and certificates](tls.md)
- [Environment reference](environment.md)
- [Production checklist](production-checklist.md)
