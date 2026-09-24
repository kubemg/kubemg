# Installing the agent

This page is the detailed reference for what the agent install actually does.
For the shortest path from zero to an attached cluster, see
[Attach your first cluster](../getting-started/first-cluster.md).

## What the install command fetches

The registration wizard's step 3 — and the cluster dashboard's **Agent
install** afterwards — renders one of two forms:

```bash
# Publicly-trusted bastion certificate
kubectl apply -f https://your-kubemg/install/<download-ticket>/agent.yaml

# Self-signed bastion certificate
curl -sfLk https://your-kubemg/install/<download-ticket>/agent.yaml | kubectl apply -f -
```

The routes are **unauthenticated by necessity** — `kubectl apply -f` cannot
carry a kubemg session — so the path carries a credential of its own. That
credential is a **single-use download ticket**, not the agent's registration
token:

- A fresh ticket is minted **every time** the install package is rendered —
  each opening of **Agent install**, each pass through the wizard's step 3,
  each **New URL** click. Opening it never changes the registration token.
- The ticket is **spent by the first download** of either form (the flat
  manifest or the Kustomize archive — both URLs carry the same ticket). A
  second fetch of the same URL answers `404`. If an apply fails after the
  download, click **New URL** (or re-open **Agent install**) and run the new
  command.
- An unused ticket **expires after 15 minutes**. The sheet states the expiry
  under the command.
- Rotating the cluster's registration token (below) withdraws every ticket
  still outstanding for that cluster.

The package the ticket downloads still carries the registration token — the
agent has to present something when it dials in — so treat the downloaded
YAML like a credential. What changes is the URL: one that lands in shell
history, a CI log or a chat message is dead after the install it was made
for. Every response carries `Cache-Control: no-store`.

!!! warning "Install URLs from before single-use downloads no longer work"
    Until single-use downloads, the install URL carried the registration token itself
    (`/install/kmg_…/agent.yaml`). Every such URL now answers `410 Gone`,
    whether or not the token in it is still valid, and names where a fresh one
    comes from. Already-installed agents are unaffected — they present the
    token from their Secret, not the URL. If an old URL may have leaked,
    [rotate the token](#rotating-the-registration-token).

- `agent.yaml` is the flat, fully-rendered manifest — a single YAML stream
  with every `__PLACEHOLDER__` filled in — for `kubectl apply -f`.
- `kustomize.tar.gz` is the same content as a Kustomize package rooted at a
  `kubemg-agent/` directory, for operators who want the
  files on disk before applying:

  ```bash
  curl -sfL https://your-kubemg/install/<token>/kustomize.tar.gz | tar -xz
  kubectl apply -k kubemg-agent
  ```

  (Kustomize only accepts local paths and Git specs as remote targets, so the
  package has to be fetched and extracted first — it cannot be applied
  straight from the URL the way the flat manifest can.)

Both are rendered from the **effective** settings, not the boot-time
environment — if an administrator changes the public URL or the
agent image in **Settings → Agent**, every install command issued afterward
reflects the change immediately, with no redeploy of the bastion.

## What lands in the cluster

```
namespace/kubemg-system
serviceaccount/kubemg-agent
secret/kubemg-agent
clusterrole/clusterrolebinding × several   (see below)
deployment/kubemg-agent
```

### Namespace

`kubemg-system` by default (`KUBEMG_AGENT_NAMESPACE`, overridable at runtime
from Settings). Labelled `app.kubernetes.io/name: kubemg-agent`,
`app.kubernetes.io/part-of: kubemg`.

### Secret (`kubemg-agent`)

```yaml
stringData:
  bastion-url: __BASTION_URL__
  cluster-token: __CLUSTER_TOKEN__
data:
  bastion-ca: __BASTION_CA__   # base64, may be empty
```

`cluster-token` is the registration secret minted at `POST /api/v1/clusters`
— it authenticates the outbound tunnel and nothing else, and revoking the
cluster in kubemg makes it useless immediately. `bastion-ca` is present but
empty when the bastion's certificate is already trusted by the agent's system
roots (a publicly-trusted certificate); when the bastion is self-signed, it
carries the PEM the agent will add to its trust store **in addition to**, not
instead of, the system roots.

### Deployment (`kubemg-agent`)

One replica, `strategy: Recreate` — the tunnel is a single long-lived socket
and the bastion keeps only the newest connection per cluster, so a second
replica would just be displaced on every reconnect. Runs as non-root
(`runAsUser: 65532`), `readOnlyRootFilesystem: true`, all capabilities
dropped, `seccompProfile: RuntimeDefault`.

```yaml
args: ["--listen=:8081"]
env:
  - KUBEMG_BASTION_URL     # from the Secret
  - KUBEMG_CLUSTER_TOKEN   # from the Secret
  - KUBEMG_BASTION_CA      # from the Secret, optional
resources:
  requests: { cpu: 50m,   memory: 48Mi }
  limits:   { cpu: 1000m, memory: 256Mi }
livenessProbe:  httpGet /healthz on :8081  # this process is up
readinessProbe: httpGet /readyz  on :8081  # the tunnel is connected
```

The CPU limit is sized for throughput rather than idle cost: the agent relays
every byte of every stream (exec, `logs -f`, port-forward) through the tunnel,
and each 32 KB chunk is a full JSON encode plus base64 plus WebSocket masking
in either direction — under an older, lower cap a dozen concurrent
port-forwards or followed logs would hit CFS throttling, and that throttling
is felt as latency across **every** shell on the cluster, not just the busy
one. Readiness tracks the tunnel rather than the process, so a cluster whose
agent cannot reach the bastion shows as not-ready in its own cluster (`kubectl
get pods`) as well as in kubemg.

### RBAC

Six ClusterRoles/ClusterRoleBindings, in `deploy/kustomize/base/rbac.yaml`:

| Object | What it grants | Bound to |
|---|---|---|
| `kubemg-agent-impersonator` | `impersonate` on `users`/`groups`/`serviceaccounts`; `get` on `/version` | the agent's own ServiceAccount |
| `kubemg-view` → built-in `view` | read-only cluster access | group `kubemg:view` |
| `kubemg-edit` → built-in `edit` | read/write, no RBAC/quota | group `kubemg:edit` |
| `kubemg-cluster-admin` → built-in `cluster-admin` | full control | group `kubemg:cluster-admin` |
| `kubemg-crd-discovery` | `get`/`list`/`watch` on `customresourcedefinitions` | group `kubemg:users` (every caller) |
| `kubemg-custom-resource-view` | `get`/`list`/`watch` on Gateway API + the five Istio API groups | group `kubemg:users` |
| `kubemg-custom-resource-edit` | `create`/`update`/`patch`/`delete`/`deletecollection` on the same groups | group `kubemg:edit` |
| `kubemg-users-discovery` → built-in `system:discovery` | lets `kubectl api-resources` resolve at all | group `kubemg:users` |

The agent itself holds almost nothing — its only privilege is the right to
*impersonate*. What an impersonated caller may actually do is decided by these
bindings and by the cluster's own RBAC, never by a standing grant the agent
holds over your workloads.

**Why CRD discovery and the custom-resource ClusterRoles exist as extras**:
the built-in `view`/`edit`/`cluster-admin` roles cover only the API groups
Kubernetes ships with. A CRD is only picked up by `view` if its author
labelled a ClusterRole `aggregate-to-view`, which most do not — so without
`kubemg-crd-discovery`, Explore's sidebar cannot even read which CRDs exist,
and without `kubemg-custom-resource-view`/`-edit` a `view` grant cannot read
the objects Gateway API or Istio actually serve.

**Why the groups are enumerated and never wildcarded**: `apiGroups: ["*"]`
would include the core group, and the core group is where Secrets live — a
blanket read there would hand every kubemg user every secret in the cluster,
which is the opposite of what a `view` grant means. To let Explore browse
another operator's CRDs (Strimzi, Debezium, an in-house CRD), add its API
group to `kubemg-custom-resource-view`/`-edit` and re-apply. The generic list
and the YAML editor already work for any CRD — RBAC is the only thing
standing between them and a kind kubemg has never heard of.

## Resource footprint

~7 MB compiled binary, no client-go dependency (only `gorilla/websocket`).
Requests 50m CPU / 48Mi memory, limited to 1000m CPU / 256Mi memory. No CRDs,
no controller, no persistent volume.

## Upgrading the agent image

The agent's image is set by `KUBEMG_AGENT_IMAGE` (default
`ghcr.io/kubemg/kubemg-agent:0.10.0`), overridable at runtime from **Settings →
Agent** without restarting the bastion. Changing it affects **future** install
and re-apply commands; an already-running agent keeps running its current
image until the manifest is re-applied.

To upgrade an attached cluster's agent:

1. Set the new image (either update `KUBEMG_AGENT_IMAGE` and restart, or
   change it in Settings at runtime).
2. Re-fetch the manifest for that cluster. In the console this is
   **the cluster's dashboard → Agent install** (admin-only, agent-mode
   clusters), which re-renders the package from the cluster's current
   registration token against the *current* settings — so it picks up the new
   image without re-registering the cluster and without invalidating anything
   already issued. Opening it mints a new single-use install URL and nothing
   else. It is offered whether or not the agent is attached, since a
   tunnel that is down is exactly when the command is needed.
3. `kubectl apply -f` (or `-k`) the freshly rendered manifest. The Deployment
   updates and, since `strategy: Recreate`, the old pod terminates before the
   new one starts — the tunnel drops and reconnects, which is normal
   operation and not treated as a failure by the bastion (a clean tunnel
   close resets the reconnect backoff to its minimum rather than penalising
   it).

!!! danger "Re-apply after every kubemg upgrade that touches RBAC"
    **Existing agent installs must re-apply their manifests** whenever the
    bastion introduces a new ClusterRole the agent depends on — this
    happened when `kubemg-crd-discovery` and
    `kubemg-custom-resource-view`/`-edit` were added. Until an install
    re-applies, **CRD discovery answers 403 and the Explore sidebar simply
    shows no custom resources**, with no other symptom: the tunnel stays up,
    the fixed inventory (pods, services, nodes, …) keeps working, and nothing
    in the console calls this out as broken. If Explore's custom-resource
    sections are unexpectedly empty on an existing cluster, re-applying the
    manifest is the first thing to try.

## Rotating the registration token

The registration token is the agent's only credential: whoever presents it
can hold that cluster's tunnel. Rotate it when it may have leaked — an old
install URL in a log, a copied Secret, a departing administrator — or on a
schedule.

**The cluster's dashboard → Rotate agent token** (admin-only, agent-mode
clusters; a direct-mode cluster answers `409`). The confirmation says what
happens, because there is **no grace window**:

1. A new token replaces the old one, and every install URL still
   outstanding for the cluster stops working.
2. The agent currently attached is **disconnected immediately** — its log
   shows `registration token rotated: re-apply the agent install package`.
   Its reconnects with the old token are refused (`unknown agent
   registration token`). On a multi-replica install, a tunnel held by
   another replica is closed within 30 seconds.
3. The **Agent install** sheet opens with the package for the new token.
   Every console session, `kubectl` call and browser shell on the cluster is
   down until that package is applied:

   ```bash
   kubectl apply -f https://your-kubemg/install/<download-ticket>/agent.yaml
   ```

   The Deployment restarts with the new Secret and the agent attaches again.

The rotation is recorded in the audit trail as `agent-token-rotate`, and
issued kubeconfigs are **not** affected — they authenticate to kubemg, not to
the agent.

## When a connection displaces the agent

A cluster has one tunnel at a time, and the newest connection wins: a rolling
agent Deployment briefly runs two pods, and the new one has to take over.
Anybody else holding the registration token takes the tunnel exactly the same
way — so every takeover is written to the audit trail as **`agent-displaced`**,
rollover or not. The record carries:

| Field | What it says |
| --- | --- |
| Source address | Where the **new** connection came from |
| Path | `/agent/v1/tunnel?` followed by the new agent's version and connection time, and the **previous** connection's address, agent version and connection time |
| Duration | How long the displaced connection had been up |

A pod your own Deployment rolled usually shares a node network with its
predecessor, runs the same agent version, and replaced a connection that had
been up for hours or days. A new address, a different version, or a
displacement seconds after a reconnect is worth a look. To be paged on it,
create an [alarm rule](../audit/alarms.md) on the audit trail with the verb
`agent-displaced`.

## Air-gapped / mirrored registries

The agent image is public on `ghcr.io/kubemg/kubemg-agent` (an amd64+arm64
manifest index, no `docker login` needed to pull it) but the **cluster**, not
the bastion, is what has to reach wherever it is pulled from. For a site with
no route to `ghcr.io`, mirror the image into an internal registry and point
`KUBEMG_AGENT_IMAGE` at it:

```dotenv
KUBEMG_AGENT_IMAGE=registry.internal/kubemg/kubemg-agent:0.10.0
```

This has to be reachable **from every cluster kubemg manages**, not from the
bastion host. There is currently no support for a private mirror that
requires authentication for the agent specifically — the rendered manifests
carry no `imagePullSecrets` — so an authenticated internal registry needs a
pull secret added to the manifest by hand before applying it. A `make
save-images` bundle for an air-gapped bundle is on the roadmap but not yet
shipped.

Building the agent image yourself:

```bash
make agent-image AGENT_VERSION=0.10.0     # builds ghcr.io/kubemg/kubemg-agent:0.10.0 locally
make agent-image-check                    # proves the amd64+arm64 matrix builds
make agent-push AGENT_VERSION=0.10.0       # requires docker login; pushes both arches
```

`REGISTRY` in the `Makefile` (default `ghcr.io/kubemg`) is what an air-gapped
site overrides to retag both kubemg images under an internal registry name.

## Uninstalling

```bash
kubectl delete -k kubemg-agent            # if applied via the Kustomize package
# or
kubectl delete -f agent.yaml              # if applied via the flat manifest
```

This removes the namespace, ServiceAccount, Secret, ClusterRoles/bindings and
Deployment — nothing else is left behind on the cluster. The cluster's
registration record and its history stay in kubemg's own database; delete the
cluster from **Admin → Clusters** separately if you want it gone from there
too (see [Managing a cluster](managing.md)).

## Troubleshooting: agent will not attach

The agent reconnects forever with exponential backoff (1s → 60s, jittered),
so a transient failure is not fatal — but a persistent one needs a fix. Check
`kubectl -n kubemg-system logs deploy/kubemg-agent` for these:

=== "x509: certificate signed by unknown authority"

    The agent could not verify the bastion's certificate. This happens when
    the bastion is self-signed or on an internal CA, but the Secret's
    `bastion-ca` key is empty or wrong — usually because the manifest was
    rendered before the bastion's certificate existed, or the wrong archive
    was applied. Re-fetch a fresh install package — **the cluster's dashboard →
    Agent install** — and re-apply it;
    the bastion's current CA is baked in at render time.

    A related, deliberately noisy line if verification is disabled by hand:

    ```
    bastion certificate verification is disabled; the tunnel can be intercepted
    ```

    This appears when `KUBEMG_BASTION_INSECURE_SKIP_VERIFY=true` is set on the
    agent — intended only for hand-running against a development bastion.

=== "agent speaks an unsupported tunnel protocol version"

    The bastion refused the handshake because the agent's tunnel protocol
    version does not match the server's. This means the
    agent image is far out of date relative to the bastion (the protocol
    version is bumped only on a breaking wire change). Re-apply the manifest
    with the current `KUBEMG_AGENT_IMAGE` to pick up a matching agent build.

=== "dial bastion: … (401 Unauthorized)" or "unknown agent registration token"

    The bearer token the agent presented does not match any cluster's stored
    registration token — the token was [rotated](#rotating-the-registration-token),
    a stale Secret from a re-registered cluster, a typo in a hand-edited
    manifest, or a cluster that was deleted and re-created (which mints a new
    token). Re-fetch the install package for the *current* cluster record and
    re-apply.

=== "registration token rotated: re-apply the agent install package"

    An administrator rotated the cluster's registration token and the bastion
    closed this tunnel. Apply the package **Agent install** now renders.

=== "this cluster is registered for direct API access, not for an agent"

    The token resolved to a real cluster, but that cluster is registered in
    **direct** mode. There is nothing to attach an agent to — either
    re-register the cluster in agent mode, or use direct mode's own health
    check instead.

=== "dial bastion: … (connection refused / timeout / i/o timeout)"

    No egress from the agent's pod to `KUBEMG_PUBLIC_URL`. Check:

    - A `NetworkPolicy` in `kubemg-system` blocking egress.
    - A corporate proxy in front of outbound HTTPS — the agent honors the
      standard Go `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` environment variables
      if you add them to the Deployment's `env`, but they are not set by
      default. Make sure `NO_PROXY` does **not** accidentally exclude the
      bastion's own host if it also has to reach cluster-internal resources
      through the proxy.
    - `KUBEMG_PUBLIC_URL` pointing at an address unreachable from *inside* the
      cluster (a `localhost` or an address that only resolves from your
      laptop) — this is the single most common cause. See
      [Attach your first cluster](../getting-started/first-cluster.md#1-make-sure-the-bastion-has-an-address-the-cluster-can-reach).

**On the bastion side**, `kubectl logs` on the kubemg server (or `make logs`
in the dev stack) shows the matching half of a failed handshake:

```
agent handshake failed  cluster=<name> error="..."
```

and a successful one:

```
agent tunnel established  cluster=<name> cluster_id=<id> agent_version=<v> kubernetes_version=<v>
```

The readiness probe (`GET /readyz` on the agent's port 8081, default `:8081`)
reports the same fact locally: `503` with body `tunnel is not connected`
whenever the tunnel is down, independent of whether the bastion is even
reachable — a stopped bastion should never make the agent report itself
*live* as failing, only *ready*, which is why liveness and readiness are
split.
