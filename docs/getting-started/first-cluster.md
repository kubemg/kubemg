# Attach your first cluster

This walks through the whole path from a bare kubemg install to a live agent
tunnel: pointing the bastion at a reachable address, registering a cluster in
agent mode, running the printed install command against the target cluster,
and watching the handshake step attach. It assumes you already have the
management plane up (see [Quickstart](quickstart.md)) and are signed in as an
administrator.

## 1. Make sure the bastion has an address the cluster can reach

Every install command kubemg renders is built from `KUBEMG_PUBLIC_URL`. It has
to be an address the **target cluster** can dial out to — not the bastion's own
listen address, and not `localhost` unless the cluster genuinely runs on the
same host as the bastion.

=== "Local cluster (minikube/kind on the same machine)"

    ```bash
    KUBEMG_PUBLIC_URL=https://host.docker.internal:8443
    ```

    `host.docker.internal` is what a container on Docker Desktop (and recent
    `minikube` drivers) resolves back to the host running the bastion. On a
    Linux host without that alias, use the host's own LAN IP instead.

=== "Cluster on the network"

    ```bash
    KUBEMG_PUBLIC_URL=https://192.0.2.10:8443
    ```

    Use the bastion's real, reachable address — an ingress hostname, a load
    balancer IP, or a NodePort address the target cluster can route to.

Set it in `.env` and bring the stack back up (`make down && make up`), or set
it at runtime from **Settings → General** without a restart — the setting
overrides the boot-time environment value and every install command rendered
after that reads the new one.

!!! warning "Self-signed CA and minikube/kind"
    The bastion mints a self-signed TLS certificate on first boot when none is
    configured (`KUBEMG_TLS_SELF_SIGNED=true` by default). That certificate is
    pinned into every rendered agent package automatically — the agent trusts
    it in addition to its system roots — so a minikube or kind cluster dialing
    a self-signed bastion works with no extra flag. What it does **not** cover
    is the one bootstrap `curl`/`kubectl apply -f` fetch of the manifest
    itself over your own machine's trust store; the rendered command already
    adds `curl -k` for that one hop when the bastion is self-signed, which is
    the only place `-k` is used.

## 2. Register the cluster in agent mode

Open **Admin → Clusters → Register cluster**. The wizard is a five-step page
at `/admin/clusters/new` (identity, connection, handshake, observability,
access):

1. **Identity** — name, environment (`prod`/`staging`/`dev`), optional
   description.
2. **Connection** — pick **Agent-based**. This is the recommended mode: kubemg
   stores no cluster credential at all, only a registration token the agent
   will present. Submitting this step is what actually creates the cluster
   record — the first two steps lock afterward.
3. **Handshake** — the wizard shows the one-line install command and starts
   polling `GET /api/v1/clusters/:id` every 3 seconds. See the next section.
4. **Observability** — optional. A cluster is fully usable with no metrics or
   logs backend registered; this step just offers to wire one up.
5. **Access** — grant a user or group a role (`view`/`edit`/`cluster-admin`)
   on the new cluster, optionally scoped to namespaces.

Details on each field and the underlying REST calls are in
[Adding a cluster](../clusters/registering.md).

## 3. Run the install command against the target cluster

Step 3 of the wizard renders a command like:

```bash
kubectl apply -f https://your-kubemg/install/<token>/agent.yaml
```

(or, over a self-signed bastion, the `curl -k … | kubectl apply -f -` form).
That URL is unauthenticated on purpose — `kubectl apply -f` cannot carry a
kubemg session, so the registration **token in the path is the credential**.
Run it with a kubeconfig context pointed at the target cluster:

```bash
kubectl config use-context my-target-cluster
kubectl apply -f https://your-kubemg/install/kmg_xxxxxxxx/agent.yaml
```

What lands in the cluster:

```
namespace/kubemg-system
serviceaccount/kubemg-agent
secret/kubemg-agent            # bastion URL, registration token, pinned CA
deployment/kubemg-agent        # one replica, ~7 MB, no CRDs
clusterrole/clusterrolebinding # impersonation, kubemg:view/:edit/:cluster-admin, CRD discovery
```

The full manifest contents and every RBAC object it creates are covered in
[Installing the agent](../clusters/agent.md).

## 4. Watch the handshake step attach

The wizard's step 3 polls the cluster record every three seconds specifically
so this step feels live — you are pasting a command into a terminal somewhere
else, and this is how you find out it worked. The moment the deployed agent
dials the bastion's tunnel listener and completes its hello/welcome handshake,
`agent_attached` on the cluster flips to `true` and the step turns green with
the agent's reported version and Kubernetes version.

If it does not attach within a few seconds of `kubectl apply` succeeding,
check:

- The agent pod is actually `Running`: `kubectl -n kubemg-system get pods`.
- The agent's logs for a dial failure — see
  [Deploying the agent → Troubleshooting](../clusters/agent.md#troubleshooting-agent-will-not-attach)
  for the exact log lines to look for (`dial bastion: …`, protocol version
  mismatches, x509 errors).
- `KUBEMG_PUBLIC_URL` really is reachable from inside the target cluster, not
  just from your laptop.

## 5. Confirm the link is live

Once attached, the cluster's row in **Operate → Fleet overview** (or the
inventory table at **Admin → Clusters**) shows a live tunnel glyph
(`Waypoints`) instead of `CircleDashed`. Opening the cluster's **Dashboard**
(`/clusters/:id/dashboard`) shows the path traffic takes — outbound tunnel,
open — and **Pods** in the header now opens Explore on live cluster state.

From here, give someone access — see
[Give someone access](first-access.md) — or continue straight to
[Browsing resources](../clusters/explore.md).
