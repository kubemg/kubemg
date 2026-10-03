# Kubernetes

The management plane installs on Kubernetes with its Helm chart, published
beside the images as an OCI artefact:

```text
oci://ghcr.io/kubemg/charts/kubemg
```

The chart's version is the release's version, and it pulls the images of that
same release — `ghcr.io/kubemg/kubemg`, and the agent and browser-shell images
the console hands out.

!!! note "This deploys the management plane, not an agent"
    The chart puts the console and gateway into *a* cluster. It is unrelated
    to the agent manifests kubemg renders per target cluster, which you apply
    to every cluster kubemg is going to manage — including, if you like, the
    same cluster the management plane runs in. See
    [Adding a cluster](../clusters/registering.md) and
    [The agent](../clusters/agent.md).

## Three decisions before the first install

The chart refuses to render until the first two are made, because an install
that came up without them would fail somewhere far less obvious.

1. **The public URL** (`publicURL`) — the `https://` address every target
   cluster's agent dials, and the one browsers and `kubectl` use. Choose it
   before the first install: the certificate kubemg mints on first boot covers
   this host and is pinned into every agent package. A plain `http://` URL is
   refused, since `kubectl` will not send a token over it.
2. **The database** — an external PostgreSQL 16 (`database.host`, the
   default), or `postgresql.enabled=true` for a single in-chart PostgreSQL
   pod meant for evaluation. The password always comes from you
   (`database.existingSecret`, or `database.password`); see
   [what the chart does not generate](#what-the-chart-does-not-generate).
3. **How agents reach it** — who terminates TLS (`tls.mode`) and what exposes
   the pod (`service.type`, `ingress`). See [TLS](#tls) and
   [Exposing it](#exposing-it).

## Install

Create the namespace and the two Secrets first, so no credential ever appears
on a command line or in a values file:

```bash
kubectl create namespace kubemg

kubectl create secret generic kubemg-db -n kubemg \
  --from-literal=DB_PASSWORD='the-database-password'

kubectl create secret generic kubemg-keys -n kubemg \
  --from-literal=KUBEMG_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=KUBEMG_SESSION_RECORDING_KEY="$(openssl rand -base64 32)"
```

`KUBEMG_SECRET_KEY` encrypts every credential kubemg stores in its database,
and `KUBEMG_SESSION_RECORDING_KEY` encrypts session recordings. **Keep a copy
of both somewhere other than the cluster:** once the secret key is set, the
server refuses to start without it. The same Secret can also carry
`KUBEMG_ADMIN_PASSWORD` and `JWT_SECRET`; every key in it is optional.

```bash
helm install kubemg oci://ghcr.io/kubemg/charts/kubemg \
  --version 0.13.0 --namespace kubemg \
  --set publicURL=https://kubemg.example.com \
  --set database.host=postgres.example.internal \
  --set database.existingSecret=kubemg-db \
  --set secrets.existingSecret=kubemg-keys \
  --set service.type=LoadBalancer
```

Then point `kubemg.example.com` at the load balancer's address
(`kubectl get service kubemg -n kubemg`) and open the public URL. The install
notes Helm prints say which TLS mode was chosen, how agents reach the pod, and
how to find the first administrator's password — generated on first boot and
printed once to the log unless you supplied `KUBEMG_ADMIN_PASSWORD`:

```bash
kubectl logs -n kubemg deploy/kubemg | grep -A6 'not configured yet'
```

### Evaluating it

For a first look, the chart can run its own PostgreSQL:

```bash
helm install kubemg oci://ghcr.io/kubemg/charts/kubemg \
  --namespace kubemg --create-namespace \
  --set publicURL=https://kubemg.lab.example \
  --set postgresql.enabled=true \
  --set database.password=evaluation-only \
  --set service.type=LoadBalancer
```

That PostgreSQL is one pod with no backup and no replication. Every agent's
registration token and the certificate every agent pinned live in it, so do
not attach clusters you care about to it.

## TLS

`tls.mode` decides who terminates TLS for the traffic agents and browsers send.

| | `passthrough` (default) | `edge` |
|---|---|---|
| Terminates TLS | kubemg | The ingress in front of it |
| The pod serves | HTTPS on 8443 | Plain HTTP on 8443 |
| The certificate agents verify | kubemg's own — minted, or yours | The ingress's |
| Exposed by | A `LoadBalancer`/`NodePort` Service, or an ingress that passes TLS through | An ingress |

**Passthrough is the recommended mode**, because there is exactly one
certificate in the whole path and it is the one agents pin. With nothing else
set, kubemg mints a self-signed certificate for the public URL's host on first
boot and pins it into every agent package. It keeps that certificate in the
database as well as the pod, so a replaced pod serves the same one — there is
no volume to lose. See
[TLS and certificates](tls.md#the-minted-certificate-is-kept-in-the-database-too).

To serve your own certificate instead, put it in a `kubernetes.io/tls` Secret
and name it in `tls.existingSecret`. cert-manager's output fits as-is:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: kubemg
  namespace: kubemg
spec:
  secretName: kubemg-tls
  dnsNames: [kubemg.example.com]
  issuerRef:
    name: corporate-ca
    kind: ClusterIssuer
```

```bash
helm upgrade kubemg oci://ghcr.io/kubemg/charts/kubemg --namespace kubemg \
  --reuse-values --set tls.existingSecret=kubemg-tls
```

What agents are told to trust then depends on who issued it:

| The certificate is | Set |
|---|---|
| Publicly trusted (an ACME issuer, a public CA) | Nothing — agents trust it through the public CAs. |
| Self-signed | Nothing — kubemg pins a self-signed certificate into agent packages on its own. |
| Issued by a private CA (cert-manager's CA issuer, a corporate PKI) | `tls.agentCABundle.existingSecret` — cert-manager writes the CA into the same Secret as `ca.crt`, so `--set tls.agentCABundle.existingSecret=kubemg-tls` is enough. |

**Changing the certificate after agents are installed is a fleet event:**
every agent verifies against what it was handed at install, so a different
certificate — or a different CA — means re-applying every agent's install
package.

In **edge** mode the pod never presents a certificate, so `tls.existingSecret`
is refused; put the certificate on the ingress (`ingress.tlsSecretName`)
instead. The chart sets `KUBEMG_TLS_ENABLED=false` and
`KUBEMG_ALLOW_INSECURE=true` for you. Agents then verify the ingress's
certificate, which kubemg never sees: if a private CA issued it, set
`tls.agentCABundle` to that CA **before installing any agent**, or every
handshake fails with an x509 error that points at the cluster rather than at
this setting.

## Exposing it

`publicURL` has to be reachable from every target cluster, and the default
`ClusterIP` Service reaches nothing outside the cluster it runs in — the
install notes warn about it.

=== "LoadBalancer (passthrough)"

    ```bash
    --set service.type=LoadBalancer
    ```

    A layer-4 load balancer passes TLS through by nature, so nothing else is
    needed. Cloud-specific settings go in `service.annotations`, and
    `service.loadBalancerSourceRanges` limits who can connect — the agents'
    egress addresses and your operators'.

=== "ingress-nginx (passthrough)"

    ```bash
    --set ingress.enabled=true --set ingress.className=nginx
    ```

    In passthrough mode the chart adds ingress-nginx's `ssl-passthrough` and
    HTTPS-backend annotations. They work only when the controller runs with
    `--enable-ssl-passthrough`; without it the controller terminates TLS with
    its own default certificate and every agent's handshake fails.

=== "Gateway API (passthrough)"

    Any other controller passes TLS through with a resource of its own. With
    Gateway API, route the host to the Service's `https` port with a
    `TLSRoute` on a listener in `Passthrough` mode:

    ```yaml
    apiVersion: gateway.networking.k8s.io/v1alpha2
    kind: TLSRoute
    metadata:
      name: kubemg
      namespace: kubemg
    spec:
      parentRefs:
        - name: shared-gateway
          sectionName: tls-passthrough
      hostnames: [kubemg.example.com]
      rules:
        - backendRefs:
            - name: kubemg
              port: 443
    ```

    Use the `TLSRoute` version your Gateway API installation serves.

=== "Ingress (edge)"

    ```bash
    --set tls.mode=edge \
    --set ingress.enabled=true --set ingress.className=nginx \
    --set ingress.tlsSecretName=kubemg-edge-tls
    ```

    The agent tunnel and interactive sessions are long-lived WebSockets.
    Raise the controller's read and send timeouts for this host (for
    ingress-nginx, `nginx.ingress.kubernetes.io/proxy-read-timeout` and
    `proxy-send-timeout` in `ingress.annotations`): a 60-second default can
    end an idle `kubectl exec` session.

## Values

`helm show values oci://ghcr.io/kubemg/charts/kubemg` prints every value with
its explanation. The ones most installs touch:

| Value | Default | What it does |
|---|---|---|
| `publicURL` | — (required) | The `https://` address agents, browsers and `kubectl` reach. |
| `tls.mode` | `passthrough` | `passthrough` or `edge` — see [TLS](#tls). |
| `tls.existingSecret` | — | A `kubernetes.io/tls` Secret to serve instead of a minted certificate. Passthrough only. |
| `tls.agentCABundle.existingSecret` / `.key` | — / `ca.crt` | The CA agents are told to trust, for a certificate a private CA issued. |
| `tls.hosts` | — | Extra names for the minted certificate. |
| `database.host` / `.port` / `.name` / `.user` / `.sslMode` | — / `5432` / `kubemg` / `kubemg` / `require` | The external PostgreSQL. |
| `database.existingSecret` / `.existingSecretPasswordKey` | — / `DB_PASSWORD` | Where the database password comes from. `database.password` renders it into the chart's own Secret instead. |
| `postgresql.enabled` | `false` | Run one PostgreSQL pod in the chart, for evaluation. |
| `secrets.existingSecret` | — | A Secret holding any of `KUBEMG_SECRET_KEY`, `KUBEMG_SESSION_RECORDING_KEY`, `KUBEMG_ADMIN_PASSWORD`, `JWT_SECRET`. |
| `admin.username` | `admin` | The first administrator. |
| `recordings.persistence.*` | on, `20Gi`, `ReadWriteOnce` | The session-recordings volume. `existingClaim` uses one you made. |
| `service.type` / `.port` | `ClusterIP` / 443 (80 in edge mode) | What exposes the pod. |
| `ingress.*` | off | See [Exposing it](#exposing-it). |
| `metrics.enabled` / `.serviceMonitor.enabled` | off / off | [Prometheus metrics](metrics.md) on their own port, behind their own `ClusterIP` Service — never the one agents reach. |
| `global.imageRegistry` | — | A mirror for every image; see [below](#mirrored-and-air-gapped-registries). |
| `imagePullSecrets` | — | For the server's pod and the in-chart PostgreSQL. |
| `agent.imagePullSecret` | — | The name of a pull secret on your **target** clusters, for the agent and browser shell images; see [Air-gapped installs](air-gapped.md#a-mirror-that-requires-authentication). |
| `extraEnv` | — | Anything else from the [environment reference](environment.md). |

## What the chart does not generate

Nothing in the chart is generated when it renders — no random password, no
certificate, nothing read back from the cluster. A render is the same under
`helm install`, `helm template`, Argo CD and Flux, which is what keeps a
GitOps sync from quietly replacing something kubemg depends on: a regenerated
database password is one PostgreSQL no longer accepts, and a regenerated
certificate is one no installed agent recognises. That is why the database
password and the keys come from you, and why the certificate kubemg mints is
minted by the server and kept in its database rather than by the chart.

It also means the chart can be rendered and applied without Helm on the
cluster:

```bash
helm template kubemg oci://ghcr.io/kubemg/charts/kubemg --version 0.13.0 \
  --namespace kubemg -f values.yaml > kubemg.yaml
kubectl apply -n kubemg -f kubemg.yaml
```

## What the pod runs as

The server runs as uid/gid `65532`, non-root, with a read-only root filesystem
and every capability dropped. Its ServiceAccount is granted nothing and its
token is not mounted: kubemg reaches clusters through agents' tunnels or the
credentials you register, never through the API server of the cluster it runs
in. It writes to two places — a memory-backed volume for the certificate's
working copy, and the recordings volume.

## Mirrored and air-gapped registries

`global.imageRegistry` replaces the registry host of every image, keeping each
repository path:

```bash
--set global.imageRegistry=registry.internal \
--set 'imagePullSecrets[0].name=registry-internal'
```

That covers the server, the in-chart PostgreSQL, and the images the console
hands out — the agent in every install package, the browser shell and the
debug container — so mirror `kubemg/kubemg`, `kubemg/kubemg-agent`,
`kubemg/kubemg-shell`, `library/postgres` and `library/busybox` under the same
paths. The agent and shell images are pulled by your **target** clusters, not
this one, so a mirror that requires authentication needs a pull secret there
too — `agent.imagePullSecret` names it; see
[Air-gapped installs](air-gapped.md#a-mirror-that-requires-authentication),
which also covers carrying the images across on physical media.

Carry the chart itself across with `helm pull
oci://ghcr.io/kubemg/charts/kubemg --version 0.13.0` and install from the
`.tgz`.

## Replicas

The chart runs **one** replica, with `strategy: Recreate`, and does not offer
a value to change it. Each agent's tunnel lives in the memory of the pod its
connection reached, so a second pod behind the same Service answers
`503 no agent tunnel is attached to this cluster` for every cluster whose
agent chose the other one — and shows those agents as not attached.
`RollingUpdate` causes the same split for the length of every rollout, and
would also stall it: the new pod cannot mount the `ReadWriteOnce` recordings
volume the old one still holds. `Recreate` costs a short outage per upgrade;
the agents reconnect on their own once the new pod is ready, each within a
minute. See [Choosing a deployment](index.md#sizing-and-high-availability)
for the full reasoning.

## Upgrading

```bash
helm upgrade kubemg oci://ghcr.io/kubemg/charts/kubemg \
  --version <new> --namespace kubemg --reuse-values
```

The schema migrates when the new pod boots. See [Upgrading](upgrading.md) for
version compatibility between the management plane and the agent.

## Uninstalling

`helm uninstall kubemg -n kubemg` removes the server, not its evidence. It
leaves the recordings claim (`kubemg-recordings`, annotated to be kept) and,
with the in-chart PostgreSQL, that database's claim; an external database is
untouched. The certificate kubemg minted lives in the database, so a new
install against the same database — and the same `KUBEMG_SECRET_KEY` — serves
the certificate every agent already pinned.

## Next

- [TLS and certificates](tls.md) — file formats, SANs, the agent trust chain
- [Environment reference](environment.md) — every variable the server reads
- [Database](database.md) — PostgreSQL sizing, backup and migrations
- [Production checklist](production-checklist.md)
