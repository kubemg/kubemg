# System architecture

kubemg is one management plane in front of many clusters. The plane holds
identity, authorisation, audit and the console; each cluster holds an agent that
dials **out** to the plane and nothing else. No cluster opens an inbound port,
and in agent mode the plane stores no cluster credential at all.

```
        browser                    kubectl / CI
           |                            |
           |  session JWT               |  proxy-scoped JWT or machine token
           v                            v
    +-------------------------------------------------+
    |                management plane                 |
    |                                                 |
    |  pkg/api       HTTP surface, authorisation      |
    |  pkg/auth      JWT, middleware                  |
    |  pkg/db        PostgreSQL 16 (GORM)             |
    |  pkg/bastion   tunnel registry + proxy          |
    |  pkg/terminal  session recording                |
    |  pkg/webui     the console, embedded            |
    +-------------------------------------------------+
             ^                          |
             | outbound WebSocket       | impersonated request
             | (the agent dials)        v
    +-------------------------------------------------+
    |  customer cluster   agent  ->  kube-apiserver   |
    +-------------------------------------------------+
```

## The pieces

| Component | Module | What it is |
|---|---|---|
| Bastion / gateway | `backend/pkg/bastion` | Holds the tunnel registry, proxies every Kubernetes call, multiplexes streams, records the trail. |
| HTTP surface | `backend/pkg/api` | Clusters, IAM, resources, Helm, observability, audit, JIT, templates. Where authorisation is decided. |
| Store | `backend/pkg/db` | Models and queries. AutoMigrate at boot. |
| Console | `frontend/` | Vite + React + TypeScript, embedded into the binary for production. |
| Agent | `agent/` | A separate Go module with one dependency. Dials out, forwards, does nothing else. |

## Four decisions everything else follows from

**The cluster dials out.** The agent opens a WebSocket to the plane and the
plane answers requests down it. That inverts the usual firewall problem: nothing
inbound, no VPN, no bastion host per cluster, no API server exposed to a
developer's laptop. [How a request flows](request-flow.md) walks one call end to
end.

**Impersonation, not per-user service accounts.** In agent mode the proxy talks
to the API server with `Impersonate-User` and `Impersonate-Group` headers, so the
cluster's own RBAC answers for the person rather than for a shared identity.
kubemg enforces *namespace scope* itself in the proxy and deliberately does not
enforce role locally — the cluster is the authority on what a role may do.

**The cluster is the primary object, not the kubeconfig.** A kubeconfig is
issued from a cluster, recorded in a register, and revocable. It is never the
thing you navigate to, and which cluster the console is reading lives in the
address rather than in page state.

**Authorisation is delegated wherever it can be, and enforced exactly once where
it cannot.** Resource reads go down the tunnel impersonated, so the cluster
answers. The one place that cannot be delegated is the observability query path:
the browser never sends a query, and the server writes PromQL, LogsQL or LogQL
around a scope derived from the caller's grant. See
[Backend internals](backend.md#the-observability-query-path).

## Two connection modes

`direct` stores an API URL and a service account token and dials the cluster
itself. `agent` is the bastion path: the cluster dials out and kubemg stores only
a registration token.

The modes are not interchangeable and the difference reaches further than it
first looks — health checks, kubeconfig contents, revocability, the browser
shell, machine accounts and in-cluster datasources all branch on it. The user
guide's [Choosing a connection mode](../clusters/connection-modes.md) is the
honest comparison, including the direct-mode gap: direct mode mints tokens
without binding a role, and agent mode is what closes it.

## Where a change usually lands

| Changing… | Start at |
|---|---|
| A route, a refusal, a payload | [Backend](backend.md) |
| The tunnel, streaming, revocation, recording | [Tunnel and proxy](bastion.md) |
| A page, a sheet, the design tokens | [Console](frontend.md) |
| Anything that runs inside a customer cluster | [The agent module](agent.md) |

Before changing an area, read its section in `ARCHITECTURE.md` at the repository
root. That file carries the alternatives that were tried and rejected, which is
the part a code comment cannot hold.
