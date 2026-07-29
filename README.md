# KubeMG

Centralized, web-based access and management for many Kubernetes clusters — a
lighter alternative to Rancher, Lens or the Kubernetes Dashboard, built around
one idea: **nobody needs network access to a cluster's API server to work with
it.**

Developers reach clusters through KubeMG. KubeMG reaches clusters through an
outbound tunnel a small in-cluster agent opens to it. Every call in between is
made under the caller's own impersonated identity, and every call is audited —
including the interactive ones, which are recorded and replayable.

```
        kubectl / browser
               │  HTTPS :443
               ▼
┌──────────────────────────────────────────────────────────┐
│ KubeMG                                                   │
│  • console (fleet, explore, IAM, audit)                   │
│  • bastion: kubectl proxy + impersonation + audit         │
│  • IAM: local users/groups, OIDC · SAML · LDAP federation │
│  • tunnel listener (WebSocket pool)                       │
└───────────────────────────▲──────────────────────────────┘
                            │ outbound tunnel, initiated by the cluster
┌───────────────────────────┴──────────────────────────────┐
│ Target cluster                                            │
│  └─ kubemg-agent (~7 MB, open source, no CRDs)            │
│       └─ https://kubernetes.default.svc                   │
└──────────────────────────────────────────────────────────┘
```

No inbound firewall rule on the cluster. No heavy in-cluster controller. In agent
mode KubeMG stores **no cluster credential at all** — only the registration token
the agent presents when it dials in.

## Why

- **Heavy agents.** Platforms like Rancher install controllers and dozens of CRDs
  and then want to own the cluster. KubeMG installs a tunnel and nothing else.
- **Desktop tools don't manage teams.** Lens is per-laptop; there is no central
  place to say who may reach production and what they may do there.
- **Handing out access is an operational wound.** Long-lived kubeconfigs get
  copied, shared and never revoked. KubeMG issues short-lived, scoped ones — and
  in agent mode the kubeconfig points at KubeMG rather than at the cluster, so
  revoking access actually revokes it.

## Security model

The parts worth reading before trusting it with production:

- **Impersonation, not shared service accounts.** The proxy calls the API server
  with `Impersonate-User` / `Impersonate-Group` headers derived from the caller's
  grant. The *cluster's own RBAC* decides what happens — a `view` grant is
  read-only because the cluster says so, not because KubeMG remembered to check.
  Client-supplied impersonation and `Authorization` headers are stripped.
- **Namespace scope is enforced in the proxy**, because it is a KubeMG concept
  that impersonation groups cannot express. A namespace-scoped grant is refused
  on anything that reaches past it, cluster-wide lists included.
- **Everything is audited, refusals included.** A long-lived call (`exec`,
  `attach`, `watch`, `logs -f`, `port-forward`) is recorded twice — when it opens
  and when it ends — so an hour-long session is visible while it is still
  running. Verbs are named after the subresource, so a shell in a production pod
  reads as `exec` and not as a `get`.
- **Interactive sessions are recorded and replayable.** Every `exec` and `attach`
  is teed into a gzipped [asciinema](https://asciinema.org) v2 cast and played
  back from the audit row it belongs to, with a keystroke view alongside the
  terminal. Non-admins can only ever reach their own sessions; deleting a
  recording is an administrative act.
- **Scoped kubeconfig tokens.** A kubeconfig lives on a laptop, so the token
  inside one is minted for exactly one cluster's proxy route and is not a session
  key for the rest of the API. Revocation works because every proxied call
  re-reads the user and the grant.
- **The bastion terminates its own TLS**, minting a self-signed certificate on
  first boot when none is configured and pinning it into every rendered agent
  package. This is not decoration: client-go refuses to send a bearer token over
  plain HTTP, so `kubectl exec` through the gateway requires TLS.
- **Reads never widen a grant.** The console's resource, metrics and Helm reads
  all go down the same impersonated, audited tunnel — the UI gets no privileged
  shortcut. Secret and ConfigMap listings return keys only; no value enters a
  response.

Two connection modes exist. `agent` is the above. `direct` — KubeMG dialling an
API server with a stored service account token — is supported, and its
**limitation is deliberate and disclosed in the UI**: KubeMG mints tokens there
but provisions no RoleBinding, so a generated kubeconfig authenticates without
authorizing, and the permission matrix governs KubeMG's own authorization rather
than the cluster's. Agent mode is where the RBAC story closes.

## What it does

**Fleet** — environment-banded cluster cards leading with the connection state,
an admin inventory, and a five-step registration wizard that waits live for the
agent to attach.

**Explore** — a resource browser over live cluster state: namespaces, workloads,
pods, services, ingresses, storage, config, nodes, plus the custom resources a
particular cluster actually serves (the sidebar is built from its own CRD list,
with first-class tables for Gateway API and Istio). One detail drawer per object
carries Overview, Describe & Events, YAML and Logs & Terminal, because finding
out something is broken, asking why, and changing it is one investigation.

**Operate** — in-browser terminal and logs (pooled across a workload's pods),
scale and restart as conditional read-modify-writes, a YAML editor, `port-forward`
over the tunnel, and Helm release values.

**Observability** — live utilisation from the cluster's own Metrics API, and
history from the datasource each cluster registers (VictoriaMetrics, Prometheus,
Thanos, Mimir, VictoriaLogs, Loki). The browser never sends a query: a caller
names a chart from a fixed catalogue and the server writes the PromQL/LogsQL
around the scope their grant allows, because a metrics backend has never heard of
the caller and will answer whatever it is asked.

**Access** — local users and groups with effective-permission merging, a
permission matrix, and federation with OIDC, SAML and LDAP including IdP group
mapping. Federated grants are revocable because their provenance is recorded.

**Audit** — a queryable trail with session replay. Readable by everyone, but a
non-admin only ever sees their own actions.

## Quick start

Everything builds and runs in containers; no Go, Node or npm on the host.

```bash
make up        # backend + frontend + Postgres
make logs      # follow
make down
```

The console comes up on <http://localhost:5173> and the API on `:8443` over
HTTPS with a self-signed certificate minted at first boot. The bootstrap admin is
`admin` / `admin` — change it, it is a development default.

To attach your first cluster: **Clusters → Register**, pick *Agent-based*, and run
the one-line `kubectl apply -k …` the wizard renders. The wizard polls until the
tunnel is up. Set `KUBEMG_PUBLIC_URL` to an address the *target cluster* can
reach — it is baked into every install command, so the container's own address
will not do.

## Configuration

Server (all optional; these are the defaults):

| Variable | Default | What it is |
|---|---|---|
| `KUBEMG_LISTEN_ADDR` | `:8080` | Listen address |
| `DB_HOST` … `DB_SSLMODE` | localhost / kubemg | PostgreSQL 16 connection |
| `JWT_SECRET`, `JWT_TTL` | dev secret, `12h` | Session signing |
| `KUBEMG_ADMIN_USERNAME` / `_PASSWORD` | `admin` / `admin` | Bootstrap admin, seeded only when the users table is empty |
| `KUBEMG_PUBLIC_URL` | `http://localhost:8080` | The outside address agents and operators reach; baked into install commands |
| `CORS_ALLOWED_ORIGINS` | Vite dev server | Where the browser app may live |
| `KUBEMG_AGENT_IMAGE`, `KUBEMG_AGENT_NAMESPACE` | pinned image, `kubemg-system` | Rendered into agent manifests |
| `KUBEMG_TLS_ENABLED` | `false` | Terminate HTTPS here. Required for `kubectl` through the proxy |
| `KUBEMG_TLS_CERT_FILE`, `_KEY_FILE` | `/etc/kubemg/tls/tls.*` | Certificate material; a self-signed pair is minted if absent |
| `KUBEMG_TLS_SELF_SIGNED`, `KUBEMG_TLS_HOSTS` | `true`, — | Whether to mint, and extra SANs |
| `KUBEMG_AGENT_CA_BUNDLE` | — | The chain agents must trust. Set it behind an ingress or an internal PKI, where nothing here can infer it |
| `KUBEMG_AUDIT_RETENTION_DAYS` | `30` | Retention for the trail *and* the recordings; also settable at runtime |
| `KUBEMG_RESOURCE_CACHE_TTL` | `5s` | Per-caller read cache; negative turns it off |
| `KUBEMG_SESSION_RECORDING_ENABLED` | `true` | Record `exec`/`attach` for replay |
| `KUBEMG_SESSION_RECORDING_DIR` | `/var/lib/kubemg/recordings` | Where casts are written. **Mount it** — recordings must outlive the container |
| `KUBEMG_SESSION_RECORDING_MAX_BYTES` | 32 MiB | Per-recording cap |

Agent: `KUBEMG_BASTION_URL`, `KUBEMG_CLUSTER_TOKEN`, `KUBEMG_BASTION_CA`
(added to the system roots, not replacing them), and
`KUBEMG_BASTION_INSECURE_SKIP_VERIFY` for hand-running against a dev bastion.
The rendered manifests set all of these for you.

## Repository layout

```
backend/            Go server: Gin + GORM + PostgreSQL 16
  pkg/bastion/        tunnel listener, kubectl proxy, streaming, audit
  pkg/api/            HTTP surface: clusters, IAM, resources, observability, audit
  pkg/terminal/       session recording (asciinema v2)
  pkg/db/             models and query layer
  pkg/auth/ k8s/ certs/ observability/ cache/ agentpkg/
frontend/           Vite + React + TypeScript + Tailwind v4
agent/              the in-cluster agent — a separate Go module
deploy/kustomize/   the agent's install manifests (human-facing copy)
```

The agent is its own module on purpose: it depends only on
`gorilla/websocket`, has no client-go, and compiles to about 7 MB. The manifests
exist twice — `deploy/kustomize/base/` for people and an embedded copy the server
renders from — and `make manifest-check` fails if they drift.

## Development

All tooling runs in containers:

```bash
make verify        # manifest-check + vet + tests + builds + frontend lint/build
make test          # backend + agent tests
make backend-test  # go test ./...
make frontend-lint
```

`docker-compose.ci.yml` exposes the same jobs as services for CI runners.

## Status

Phases 1–4 are in place: multi-cluster IAM, the bastion and agent, the
single-pane-of-glass console, and enterprise SSO federation. Phase 5 is under way
— session recording and replay has landed; JIT elevated access, command
guardrails, RCA assistance, topology and FinOps views are next. The auto-provisioned
VictoriaMetrics/VictoriaLogs stack is not built yet; bring your own for now.

## Licensing

There is no `LICENSE` file in this repository yet, which means no rights are
granted by default. The in-cluster agent (`agent/`, `deploy/kustomize/`) is
intended to be open source so SecOps teams can read exactly what runs on their
clusters; the server and console are a commercial product. Add the licence texts
that match that split before treating anything here as reusable.

## Security

Please report vulnerabilities privately to the maintainer rather than opening a
public issue. If you are evaluating KubeMG for production, the security model
section above is the honest short version — including the direct-mode limitation.
