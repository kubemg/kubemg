# Kubernetes

Install the management plane on Kubernetes with the Helm chart: make three
decisions, create two Secrets, run `helm install`. The chart is published as an
OCI artefact:

```text
oci://ghcr.io/kubemg/charts/kubemg
```

The chart's version is the release's version and it pulls that release's images.

!!! note "This deploys the management plane, not an agent"
    The chart installs the console and gateway. It is unrelated to the agent
    manifests kubemg renders per target cluster (apply those to every managed
    cluster, including this one if you like). See
    [Adding a cluster](../clusters/registering.md) and
    [The agent](../clusters/agent.md).

## Three decisions before the first install

The chart refuses to render until the first two are made.

1. **The public URL** (`publicURL`) — the `https://` address every target
   cluster's agent dials, and the one browsers and `kubectl` use. Choose it
   first: the minted certificate covers this host and is pinned into every
   agent package. Plain `http://` is refused.
2. **The database** — an external PostgreSQL 16 (`database.host`, the
   default), or `postgresql.enabled=true` for a single in-chart PostgreSQL
   pod meant for evaluation. The password always comes from you
   (`database.existingSecret`, or `database.password`); see
   [what the chart does not generate](#what-the-chart-does-not-generate).
3. **How agents reach it** — who terminates TLS (`tls.mode`) and what exposes
   the pod (`service.type`, `ingress`). See [TLS](#tls) and
   [Exposing it](#exposing-it).

## Install

Create the namespace and two Secrets first, so no credential appears in a
values file:

```bash
kubectl create namespace kubemg

kubectl create secret generic kubemg-db -n kubemg \
  --from-literal=DB_PASSWORD='the-database-password'

kubectl create secret generic kubemg-keys -n kubemg \
  --from-literal=KUBEMG_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=KUBEMG_SESSION_RECORDING_KEY="$(openssl rand -base64 32)"
```

`KUBEMG_SECRET_KEY` encrypts stored credentials and
`KUBEMG_SESSION_RECORDING_KEY` encrypts recordings. **Keep a copy of both
outside the cluster:** the server will not start without the secret key. The
Secret may also carry `KUBEMG_ADMIN_PASSWORD` and `JWT_SECRET`; every key is
optional.

```bash
helm install kubemg oci://ghcr.io/kubemg/charts/kubemg \
  --version 0.14.0 --namespace kubemg \
  --set publicURL=https://kubemg.example.com \
  --set database.host=postgres.example.internal \
  --set database.existingSecret=kubemg-db \
  --set secrets.existingSecret=kubemg-keys \
  --set service.type=LoadBalancer
```

Point `kubemg.example.com` at the load balancer
(`kubectl get service kubemg -n kubemg`) and open the public URL. Helm's notes
state the TLS mode and how agents reach the pod. The first admin password is
printed once to the log unless you set `KUBEMG_ADMIN_PASSWORD`:

```bash
kubectl logs -n kubemg deploy/kubemg | grep -A6 'not configured yet'
```

### Evaluating it

For a first look the chart can run its own PostgreSQL:

```bash
helm install kubemg oci://ghcr.io/kubemg/charts/kubemg \
  --namespace kubemg --create-namespace \
  --set publicURL=https://kubemg.lab.example \
  --set postgresql.enabled=true \
  --set database.password=evaluation-only \
  --set service.type=LoadBalancer
```

That PostgreSQL is one pod with no backup or replication, and it holds every
agent token and the pinned certificate. Do not attach clusters you care about.

## TLS

`tls.mode` decides who terminates TLS for the traffic agents and browsers send.

| | `passthrough` (default) | `edge` |
|---|---|---|
| Terminates TLS | kubemg | The ingress in front of it |
| The pod serves | HTTPS on 8443 | Plain HTTP on 8443 |
| The certificate agents verify | kubemg's own — minted, or yours | The ingress's |
| Exposed by | A `LoadBalancer`/`NodePort` Service, or an ingress that passes TLS through | An ingress |

**Passthrough is recommended**: one certificate in the whole path, the one
agents pin. By default kubemg mints a self-signed certificate for the public
URL's host, pins it into agent packages and keeps it in the database, so a
replaced pod serves the same one. See
[TLS and certificates](tls.md#the-minted-certificate-is-kept-in-the-database-too).

To serve your own, put it in a `kubernetes.io/tls` Secret and set
`tls.existingSecret`. cert-manager's output fits as-is:

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

**Changing the certificate (or CA) after agents are installed means
re-applying every agent's install package.**

In **edge** mode `tls.existingSecret` is refused; put the certificate on the
ingress (`ingress.tlsSecretName`). The chart sets `KUBEMG_TLS_ENABLED=false`
and `KUBEMG_ALLOW_INSECURE=true`. If a private CA issued the ingress
certificate, set `tls.agentCABundle` to that CA **before installing any
agent**, or every handshake fails with an x509 error that points at the
cluster.

## Exposing it

`publicURL` must be reachable from every target cluster; the default
`ClusterIP` Service is not.

=== "LoadBalancer (passthrough)"

    ```bash
    --set service.type=LoadBalancer
    ```

    A layer-4 load balancer passes TLS through. Cloud settings go in
    `service.annotations`; `service.loadBalancerSourceRanges` limits who can
    connect (agents' egress and your operators').

=== "ingress-nginx (passthrough)"

    ```bash
    --set ingress.enabled=true --set ingress.className=nginx
    ```

    The chart adds ingress-nginx's `ssl-passthrough` and HTTPS-backend
    annotations. They need the controller to run with
    `--enable-ssl-passthrough`; without it every agent handshake fails.

=== "Gateway API (passthrough)"

    Route the host to the Service's `https` port with a `TLSRoute` on a
    `Passthrough` listener:

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

    The tunnel and interactive sessions are long-lived WebSockets. Raise the
    controller's timeouts for this host (ingress-nginx:
    `nginx.ingress.kubernetes.io/proxy-read-timeout` and `proxy-send-timeout`
    in `ingress.annotations`); a 60-second default can end an idle
    `kubectl exec`.

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

Nothing is generated at render time: no random password, no certificate,
nothing read from the cluster. A render is identical under `helm install`,
`helm template`, Argo CD and Flux, so a GitOps sync never replaces a password
or certificate kubemg depends on. The database password and keys come from you;
the certificate is minted by the server and kept in its database.

So you can also render and apply without Helm on the cluster:

```bash
helm template kubemg oci://ghcr.io/kubemg/charts/kubemg --version 0.14.0 \
  --namespace kubemg -f values.yaml > kubemg.yaml
kubectl apply -n kubemg -f kubemg.yaml
```

## What the pod runs as

uid/gid `65532`, non-root, read-only root filesystem, all capabilities
dropped. Its ServiceAccount is granted nothing and its token is not mounted.
It writes only to a memory-backed volume (certificate working copy) and the
recordings volume.

## Mirrored and air-gapped registries

`global.imageRegistry` replaces the registry host of every image, keeping each
repository path:

```bash
--set global.imageRegistry=registry.internal \
--set 'imagePullSecrets[0].name=registry-internal'
```

This covers the server, in-chart PostgreSQL and the images the console hands
out (agent, browser shell, debug container). Mirror all five under the same
paths, and for authenticated mirrors set `agent.imagePullSecret`. See
[Air-gapped installs](air-gapped.md), which also covers physical media and
carrying the chart across (`helm pull`).

## Replicas

The chart runs **one** replica with `strategy: Recreate`; there is no value to
change it. Each upgrade costs a short outage, and agents reconnect on their own
within a minute.

??? info "Why it works this way"
    An agent's tunnel lives in the memory of the pod it reached, so a second
    pod answers `503 no agent tunnel is attached to this cluster` for clusters
    whose agent chose the other one. `RollingUpdate` causes the same split
    during every rollout and stalls, because the new pod cannot mount the
    `ReadWriteOnce` recordings volume. See
    [Choosing a deployment](index.md#sizing-and-high-availability).

## Upgrading

```bash
helm upgrade kubemg oci://ghcr.io/kubemg/charts/kubemg \
  --version <new> --namespace kubemg --reuse-values
```

The schema migrates when the new pod boots. See [Upgrading](upgrading.md) for
agent compatibility.

## Uninstalling

`helm uninstall kubemg -n kubemg` removes the server, not its evidence. It
leaves the recordings claim (`kubemg-recordings`) and, with the in-chart
PostgreSQL, that database's claim; an external database is untouched. A new
install against the same database and `KUBEMG_SECRET_KEY` serves the
certificate agents already pinned.

## Next

- [TLS and certificates](tls.md) — file formats, SANs, the agent trust chain
- [Environment reference](environment.md) — every variable the server reads
- [Database](database.md) — PostgreSQL sizing, backup and migrations
- [Production checklist](production-checklist.md)
