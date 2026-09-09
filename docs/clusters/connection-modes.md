# Connection modes

Every cluster kubemg manages is registered in one of two modes,
`Cluster.ConnectionMode` in the schema: **agent** or **direct**. The mode is
fixed at registration and cannot be changed afterward — switching means
registering the cluster again.

## Agent mode

The cluster runs the open-source **kubemg-agent** inside it. The agent dials
**out** to the bastion and holds a WebSocket tunnel open; kubemg never
initiates a connection to the cluster.

- **What kubemg stores**: nothing that reaches the cluster. Only the
  registration token the agent presents when it dials in
  (`Cluster.AgentToken`), which authenticates the tunnel and nothing else.
- **What dials what**: the agent dials the bastion. No inbound firewall rule,
  no exposed API server, no route from kubemg into the cluster's network is
  required.
- **How health is checked**: kubemg asks the tunnel pool whether this cluster's
  agent is currently connected. There is nothing to probe over the network — the whole point of agent mode
  is that kubemg has no route to dial.
- **Kubeconfig**: `generateKubeconfig` points the file at kubemg's own proxy
  (`{public URL}/api/v1/clusters/:id/proxy`) and embeds a kubemg-issued JWT
  scoped to that one cluster's proxy route, never a
  cluster-native token. The bastion's own CA is pinned into the file
  whenever the bastion is self-signed, because in this mode the "cluster"
  `kubectl` is dialling is kubemg, not the target API server.
- **RBAC**: closes the gap direct mode leaves open (see below). The agent's
  installed manifests bind `kubemg:view`/`kubemg:edit`/`kubemg:cluster-admin`
  to the built-in ClusterRoles, and the proxy asserts the caller with
  `Impersonate-User`/`Impersonate-Group`, so **the cluster's own RBAC decides**
  what a grant is actually worth.

Agent mode is the recommended path and the one the registration wizard
defaults to.

## Direct mode

kubemg dials the cluster's API server itself, the way any `kubectl` client
would.

- **What kubemg stores**: `api_url`, an optional `ca_cert_data`, and a
  **service account token** (`service_account_token`) — a real, standing
  credential for the target cluster, held in kubemg's database.
- **What dials what**: kubemg dials the cluster's API server directly. This
  needs a network path from the bastion to the cluster and, in most
  deployments, an exposed API server.
- **How health is checked**: kubemg runs an actual probe against the stored
  `api_url`, since there is a real connection to test.
- **Kubeconfig**: kubemg mints a short-lived token straight from
  the cluster's own TokenRequest API, using the stored service account. The
  file points directly at the cluster's `api_url`.
- **RBAC — the known gap**: kubemg mints tokens here but provisions **no
  RoleBinding**. A generated kubeconfig authenticates without authorizing:
  whatever the stored service account was already bound to inside the cluster
  is what a caller gets, and **the permission matrix governs kubemg's own
  authorization rather than the cluster's**. A `view` grant in kubemg does not
  make anything read-only inside the cluster in direct mode — that enforcement
  simply is not there. This is deliberate and disclosed in the UI (the
  cluster's dashboard, the permissions page, and the wizard's last step all
  state which of the two applies), not a bug to be quietly patched around.

## Decision table

| | Agent | Direct |
|---|---|---|
| Cluster credential stored in kubemg | None (only a registration token) | API URL + service account token |
| Inbound port required on the cluster | No | Usually yes (exposed API server) |
| Who initiates the connection | The cluster, outbound | kubemg, inbound to the cluster |
| Health check | Tunnel pool connectivity | Live probe against the API server |
| Kubeconfig points at | kubemg's own proxy | The cluster's API server directly |
| Kubeconfig credential | kubemg-issued, proxy-scoped JWT | Cluster-native TokenRequest token |
| Cluster RBAC enforced on a grant | Yes — impersonation + bound ClusterRoles | No — kubemg's permission matrix only |
| Revocation | Immediate (tunnel re-reads grant every call) | Only as fast as the token expires |
| Explore custom-resource discovery, exec/logs/port-forward, session recording | All available | Not available — there is no tunnel to carry them |

## Full capability matrix

The row above about Explore, streaming and recording understates how far the
split actually goes: **every** `/api/v1/clusters/:id/resources/*`, `/metrics/*`
and `/observability/*/query` read is served through the tunnel, and a
direct-mode cluster is refused outright with

> this cluster is registered for direct API access; generate a kubeconfig instead

— so a direct-mode cluster has **no** Explore, no live metrics tab, no
resource list, no describe, no events, and no in-console query path at all.
Everything kubemg can show you about a direct-mode cluster comes from the
health probe (name, reachability, Kubernetes version) and whatever `kubectl`
you run yourself against the kubeconfig it hands you. This is the reason the
"Explore" row above is not a minor feature gap: it is the whole console
surface.

| Capability | Agent | Direct |
|---|---|---|
| Health check | Tunnel presence — is the agent connected right now | A live probe against `api_url` |
| Kubeconfig issuance | Always available | Available, but the stored service account token must be able to issue one |
| RBAC enforcement | The cluster's own RBAC, via `Impersonate-User`/`Impersonate-Group` and the bound `kubemg:view`/`kubemg:edit`/`kubemg:cluster-admin` ClusterRoles | None provisioned by kubemg — a `view` grant does not make anything read-only inside the cluster |
| Revocation latency | Immediate — the proxy re-reads the grant on every call | Bounded only by the token's TTL, which the cluster's own `--service-account-max-token-expiration` may shorten further |
| Explore (resource lists, describe, events, YAML editor, custom resources) | Available | Refused with `409 this cluster is registered for direct API access; generate a kubeconfig instead` — there is no tunnel to carry the call |
| `exec` / `attach` / `logs -f` / `port-forward` | Available, multiplexed over the tunnel | Not reachable through kubemg at all; falls entirely to the kubeconfig and your own `kubectl` |
| Session recording | Every `exec`/`attach` through the proxy is teed into a cast | Nothing to record — the shell never passes through kubemg |
| Machine accounts / programmatic tokens | Supported | Refused with `409`: *"programmatic access needs a cluster registered in agent mode. In direct mode the credential is minted on the cluster itself, so kubemg cannot revoke it and the cluster's RBAC has nothing bound to it."* |
| In-cluster observability source (Prometheus/Loki/VictoriaMetrics reached via the API server's Service proxy) | Supported | Refused with `409`: *"an in-cluster datasource is reached through the agent tunnel, which a direct-mode cluster does not have — give its external address instead"*. A `direct`-access datasource (a URL the bastion can dial itself) still works. |
| Alarm event polling | Available — reads cluster Events down the tunnel | Not available — there is no tunnel to read from |
| Network requirement | Outbound HTTPS from the cluster to the bastion's public URL; nothing inbound | A route from the bastion to `api_url`, and in most deployments an exposed API server reachable from wherever the bastion runs |
| What kubemg stores for this cluster | `Cluster.AgentToken` (registration token only) | `api_url`, `ca_cert_data`, and `service_account_token` — a standing, usable credential for the cluster, held in kubemg's own database |

## Which should I use

**Use agent mode unless you have a specific reason not to.** It is the only
mode where kubemg's grants mean anything to the cluster itself, it needs no
inbound network change, it is the only mode that carries `exec`, `attach`,
`logs -f`, `port-forward`, session recording, and CRD-based Explore discovery,
and revocation actually revokes access rather than merely being waited out.

Direct mode exists as the Phase 1 path — register a cluster you already have
API access to without deploying anything into it — and remains useful for a
quick look at a cluster's health and its resources under kubemg's own
authorization model. It is not a fit for anything where "kubemg's `view` grant
must really be read-only inside the cluster" matters, because it is not.

See [Installing the agent](agent.md) for what agent mode actually installs, and
[Adding a cluster](registering.md) for the step-by-step wizard flow in
both modes.

## Moving a cluster from direct mode to agent mode

There is no in-place switch. `Cluster.ConnectionMode` is set once, at
`createCluster`, and no update route touches it — a `PUT` on a cluster cannot
change it, and nothing in the store layer offers to. Moving a cluster means
retiring the direct-mode registration and creating a new agent-mode one. In
order:

1. **Deploy the agent first, registration second.** From `/clusters/new`,
   run through the wizard choosing **agent** mode. This creates a *new*
   cluster row with a *new* cluster ID and mints a fresh registration token
   — it does not touch the existing direct-mode row. Step 3 of the wizard
   gives you the install command; run it against the same physical cluster
   and wait for the handshake to complete (the wizard polls `GET
   /clusters/:id` every three seconds and stops the moment the agent
   attaches).
2. **Re-create access grants on the new cluster ID.** `user_cluster_access`
   and `group_cluster_access` rows key on `cluster_id`, so nothing carries
   over automatically — a grant on the old direct-mode cluster says nothing
   about the new agent-mode one. Recreate the same grants (same users or
   groups, same `k8s_role`, same namespaces) against the new cluster from
   [Permissions](../access/model.md) or the group editor. If you also had
   [guardrail policies](../access/guardrails.md) scoped to the old cluster
   by name or ID, re-point them.
3. **Re-issue every kubeconfig.** A kubeconfig generated for the old,
   direct-mode cluster carries the cluster's own `api_url` as its server and
   a token minted straight from that cluster's TokenRequest API — it has
   nothing to do with kubemg's proxy and keeps working, against the same
   physical cluster, until it expires on its own, entirely outside kubemg's
   control. It does **not** automatically start behaving like an agent-mode
   file. Anyone who needs the new properties (impersonation, instant
   revocation, the bastion's own TLS) needs a kubeconfig generated against
   the *new* cluster ID, which points at `{public URL}/api/v1/clusters/:id/proxy`
   instead of the cluster's own address.
4. **Re-check anything cluster-scoped by ID**: machine account grants,
   observability datasources (an in-cluster source can now be registered,
   where it was refused before), consoles (`cluster_consoles`), and any
   saved audit-trail filter or bookmark that names the old cluster ID —
   these all live on the ID, not the cluster's name.
5. **Retire the old registration.** Once traffic has moved to the new
   cluster ID, delete the direct-mode row (`DELETE /api/v1/clusters/:id`).
   Nothing is deleted implicitly by registering the replacement — the two
   rows coexist, pointing at the same physical cluster, until you remove one.

Because the two rows are entirely independent registrations rather than one
record changing shape, treat this as decommissioning one cluster and
onboarding another that happens to be the same Kubernetes control plane —
not as an edit.
