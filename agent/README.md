# KubeMG Dumb Agent

The open-source half of KubeMG. It runs in your cluster, opens one **outbound**
tunnel to a KubeMG bastion, and replays the requests that arrive down it against
your own API server. That is the whole job.

It is called *dumb* on purpose:

- it makes no authorization decisions — the bastion decides who may do what, and
  asserts that identity with `Impersonate-User` / `Impersonate-Group` headers,
  so your cluster's own RBAC remains the authority;
- it installs no CRDs and runs no controllers;
- it caches no cluster state;
- it opens no listening port to the network — only a loopback-shaped health
  endpoint on `:8081` for the kubelet's probes.

Everything it can do is bounded by the ClusterRole in
`deploy/kustomize/base/rbac.yaml`, which grants exactly one privilege:
`impersonate` on users and groups.

## Installing

Register the cluster in KubeMG (**Clusters → Add cluster → Agent-based**) and
run the command the wizard gives you. See
[`deploy/kustomize/README.md`](../deploy/kustomize/README.md) for the manifests
and for the GitOps route.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `KUBEMG_BASTION_URL` | *(required)* | Public URL of the KubeMG server |
| `KUBEMG_CLUSTER_TOKEN` | *(required)* | This cluster's registration token |
| `KUBEMG_KUBERNETES_URL` | `https://kubernetes.default.svc` | API server address |
| `KUBEMG_LISTEN_ADDR` | `:8081` | Health and readiness probes |
| `KUBEMG_INSECURE_SKIP_VERIFY` | `false` | Skip API server certificate verification — development only |

The same values are available as flags (`--kubernetes-url`, `--listen`,
`--insecure-skip-verify`).

`/healthz` reports the process; `/readyz` reports the tunnel, so a pod that
cannot reach the bastion shows as not-ready in your cluster as well as in
KubeMG.

## Building

Everything runs in containers — no host Go toolchain needed. From the repository
root:

```
make agent-test     # go test ./...
make agent-vet      # go vet ./...
make agent-build    # static CGO_ENABLED=0 binary
make agent-image    # distroless container image
```

## Protocol

One WebSocket carries JSON frames, multiplexed by correlation ID:

| Frame | Direction | Meaning |
| --- | --- | --- |
| `hello` | agent → bastion | protocol version, agent version, Kubernetes version |
| `welcome` | bastion → agent | which cluster the tunnel bound to |
| `request` | bastion → agent | one HTTP call to replay against the API server |
| `response` | agent → bastion | the API server's answer, or why there isn't one |

The agent authenticates with its registration token as a bearer token on the
upgrade request; the cluster identity is derived from that token, so an agent
cannot claim to be a cluster it holds no token for.

`internal/protocol` mirrors the bastion's `pkg/bastion/protocol.go`. The two are
separate files because the agent is a separate, open-source module — they agree
on JSON field names and on `ProtocolVersion`, which the bastion checks during the
handshake.

### Not yet carried

`exec`, `attach`, `port-forward`, `watch` and `logs -f` need a bidirectional or
long-lived stream that this framing does not provide. The bastion refuses them
with `501 Not Implemented` rather than letting them hang. They land with the
streaming protocol in Phase 3.
