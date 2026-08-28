# Kubernetes

There is no published Helm chart yet (`roadmap.md` lists it as planned, not
shipped), so a Kubernetes install of the management plane means applying
plain manifests against the image at the repository root's `Dockerfile`,
published as `ghcr.io/kubemg/kubemg`. This page is a complete, working set to
start from.

!!! note "This deploys the management plane, not an agent"
    These manifests put the console+gateway binary into *a* cluster. They are
    unrelated to the agent manifests kubemg renders per target cluster
    (`deploy/kustomize/`, `kubectl apply -k https://.../install/<token>/kustomize.tar.gz`),
    which you apply to every cluster kubemg is going to manage — including,
    if you like, the same cluster the management plane runs in. See
    [Registering a cluster](../clusters/registering.md) and
    [The agent](../clusters/agent.md).

## What the container needs

From the repository root `Dockerfile` and `backend/pkg/config/config.go`:

- Listens on **`:8443`** with TLS enabled by default (`KUBEMG_LISTEN_ADDR`,
  `KUBEMG_TLS_ENABLED=true` are baked in as image defaults).
- Runs as **uid/gid `65532`** (the distroless `nonroot` base image's user) —
  any volume it writes to needs to be writable by that uid.
- Writes to two directories that must be persistent: `/etc/kubemg/tls` (the
  certificate it mints itself, unless you supply one) and
  `/var/lib/kubemg/recordings` (session recordings). Losing either — a
  certificate every already-installed agent has pinned, or audit evidence —
  is a real incident, not a redeploy.
- Optionally reads a certificate from `/etc/kubemg/ssl` — see
  [TLS and certificates](tls.md) for exact filenames and format.
- Answers `GET /health` with a plain `200 {"status":"ok"}`, unauthenticated,
  over HTTPS (since TLS is on by default). Kubernetes probes with
  `scheme: HTTPS` do not validate the serving certificate, so a self-signed
  one is fine for a probe.
- PostgreSQL 16 and its own boot-time defaults come entirely from environment
  variables — see the [environment reference](environment.md).

## Namespace, Secret, PVCs

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kubemg
---
apiVersion: v1
kind: Secret
metadata:
  name: kubemg-secrets
  namespace: kubemg
type: Opaque
stringData:
  DB_PASSWORD: "change-me"
  # Signs sessions, generated kubeconfigs and JIT approval links. Left unset,
  # each replica mints and stores its own in the database — set it explicitly
  # once you run more than one replica. Generate with: openssl rand -base64 48
  JWT_SECRET: "change-me"
  # Optional: leave unset and a password is generated and printed once to the
  # pod's log instead (`kubectl logs -n kubemg deploy/kubemg | grep -A6 'not configured yet'`).
  KUBEMG_ADMIN_PASSWORD: ""
  # Encrypts session recordings at rest. Unset, recordings are written in
  # plaintext and the server warns at boot. Generate with: openssl rand -base64 32
  KUBEMG_SESSION_RECORDING_KEY: "change-me"
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: kubemg-tls
  namespace: kubemg
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: kubemg-recordings
  namespace: kubemg
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 20Gi
```

If you're supplying your own certificate rather than letting kubemg mint one,
add a second Secret for it and mount it read-only at `/etc/kubemg/ssl` instead
of (or alongside) the `kubemg-tls` PVC — see [TLS and certificates](tls.md).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kubemg-tls-supplied
  namespace: kubemg
type: kubernetes.io/tls
data:
  tls.crt: <base64>
  tls.key: <base64>
```

## Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubemg
  namespace: kubemg
spec:
  # See "Replicas" below before scaling past 1.
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: kubemg
  template:
    metadata:
      labels:
        app.kubernetes.io/name: kubemg
    spec:
      securityContext:
        # The image runs as uid/gid 65532; fsGroup makes the PVCs writable by it.
        fsGroup: 65532
      containers:
        - name: kubemg
          image: ghcr.io/kubemg/kubemg:0.8.0
          ports:
            - name: https
              containerPort: 8443
          env:
            - name: DB_HOST
              value: postgres.kubemg.svc.cluster.local
            - name: DB_PORT
              value: "5432"
            - name: DB_USER
              value: kubemg
            - name: DB_NAME
              value: kubemg
            - name: DB_SSLMODE
              value: require
            - name: DB_PASSWORD
              valueFrom:
                secretKeyRef: { name: kubemg-secrets, key: DB_PASSWORD }
            - name: JWT_SECRET
              valueFrom:
                secretKeyRef: { name: kubemg-secrets, key: JWT_SECRET }
            - name: KUBEMG_ADMIN_PASSWORD
              valueFrom:
                secretKeyRef: { name: kubemg-secrets, key: KUBEMG_ADMIN_PASSWORD, optional: true }
            - name: KUBEMG_SESSION_RECORDING_KEY
              valueFrom:
                secretKeyRef: { name: kubemg-secrets, key: KUBEMG_SESSION_RECORDING_KEY }
            # The address TARGET CLUSTERS dial — not this Service's ClusterIP.
            # See "Exposing it" below.
            - name: KUBEMG_PUBLIC_URL
              value: "https://kubemg.example.com"
            - name: KUBEMG_TLS_HOSTS
              value: "kubemg.example.com"
            - name: KUBEMG_SESSION_RECORDING_DIR
              value: /var/lib/kubemg/recordings
          volumeMounts:
            - name: tls
              mountPath: /etc/kubemg/tls
            - name: recordings
              mountPath: /var/lib/kubemg/recordings
            # Only if you added the supplied-certificate Secret above.
            # - name: tls-supplied
            #   mountPath: /etc/kubemg/ssl
            #   readOnly: true
          readinessProbe:
            httpGet:
              path: /health
              port: https
              scheme: HTTPS
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /health
              port: https
              scheme: HTTPS
            initialDelaySeconds: 10
            periodSeconds: 20
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: "1"
              memory: 512Mi
      volumes:
        - name: tls
          persistentVolumeClaim: { claimName: kubemg-tls }
        - name: recordings
          persistentVolumeClaim: { claimName: kubemg-recordings }
        # - name: tls-supplied
        #   secret: { secretName: kubemg-tls-supplied }
---
apiVersion: v1
kind: Service
metadata:
  name: kubemg
  namespace: kubemg
spec:
  selector:
    app.kubernetes.io/name: kubemg
  ports:
    - name: https
      port: 8443
      targetPort: https
```

No `CORS_ALLOWED_ORIGINS` is set: the console is served by the same
container, from the same origin as the API it calls, so there is nothing
cross-origin to permit in a production install.

## Exposing it, and what that means for TLS

`KUBEMG_PUBLIC_URL` has to be the address a **target cluster's agent** — not
this cluster's internal DNS — can dial. That means an Ingress (or a
`LoadBalancer` Service) in front of it, and that raises the one question that
matters more here than anywhere else: **who terminates TLS, kubemg or the
ingress?**

=== "Passthrough (recommended)"

    Let the pod keep terminating its own TLS and have the ingress controller
    pass the raw TCP stream through untouched (e.g. nginx-ingress's
    `nginx.ingress.kubernetes.io/ssl-passthrough: "true"`, on an Ingress that
    routes by SNI rather than by path). Nothing changes in the Deployment
    above: `KUBEMG_TLS_ENABLED` stays `true`, and the certificate every agent
    pins is the one kubemg itself minted or was given. This is simplest
    because there is exactly one certificate in the whole path.

=== "Terminate at the ingress"

    If your ingress controller terminates TLS itself and forwards plaintext
    to the Service, the pod must be told to stop insisting on TLS *and* that
    doing so is intentional — its port is reachable from more than this one
    machine, so it refuses to start otherwise:

    ```yaml
    - name: KUBEMG_TLS_ENABLED
      value: "false"
    - name: KUBEMG_ALLOW_INSECURE
      value: "true"
    ```

    And critically: the certificate an **agent** verifies is now the
    ingress's, not one this process ever sees — nothing here can infer that
    chain automatically. Set `KUBEMG_AGENT_CA_BUNDLE` to the CA bundle the
    ingress presents (a PEM file, mounted from a Secret or ConfigMap), or
    every agent's handshake fails with an x509 error that points at the
    *cluster*, not at this setting. See
    [the agent trust story](tls.md#agent-trust-the-agent_ca_bundle) for detail.

Either way, `readinessProbe`/`livenessProbe` above target the pod directly
over its own port, so they're unaffected by which side of the ingress
terminates TLS for real traffic.

## Replicas

The management plane is close to stateless — see
[Choosing a deployment](index.md#sizing-and-high-availability) for the full
reasoning. In short: set `JWT_SECRET` explicitly before running more than one
replica (otherwise each mints its own key independently before agreeing on
one in the database), put `/etc/kubemg/tls` and the recordings directory on
volumes every replica can read consistently (`ReadWriteMany`, or an external
object store fronted appropriately, if you need more than one replica and a
single `ReadWriteOnce` PVC won't attach to more than one node), and don't
worry about the cluster-event alarm poller — it self-elects a single replica
via a database lease regardless of how many you run.

## Next

- [TLS and certificates](tls.md) — file formats, SANs, the agent trust chain
- [Environment reference](environment.md) — every variable this image reads
- [Database](database.md) — PostgreSQL sizing and migrations
- [Production checklist](production-checklist.md)
