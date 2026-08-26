# The agent

`agent/` is the open-source half of kubemg: a separate Go module, licensed
Apache-2.0, that runs inside a managed cluster and does exactly one job —
hold an outbound tunnel to the bastion and replay whatever arrives down it
against the cluster's own API server.

## What it does, and deliberately does not

It is called the *Dumb Agent* on purpose:

- It makes **no authorization decisions** — the bastion decides who may do
  what, and asserts that identity with `Impersonate-User`/`Impersonate-Group`
  headers, so the cluster's own RBAC remains the authority.
- It installs **no CRDs** and runs **no controllers**.
- It **caches no cluster state**.
- It opens **no listening port to the network** — only a loopback-shaped
  health endpoint on `:8081` for the kubelet's own probes.

Its `go.mod` depends on exactly one third-party package,
`github.com/gorilla/websocket` — no `client-go`, which is what keeps the
binary small and its dependency surface auditable at a glance. Everything it
is permitted to do inside the cluster is bounded by the ClusterRole in
`deploy/kustomize/base/rbac.yaml`, which grants exactly one privilege:
`impersonate` on users and groups.

## Size

The build is `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"`, run on
`golang:1.26-alpine` and shipped on `gcr.io/distroless/static-debian12:nonroot`
— a static ~10 MB binary with no libc, no shell, and nothing else on the
image to audit but the binary itself. It runs as `65532:65532` (`nonroot`),
matching the `runAsUser` in the deployment manifest.

## The tunnel and the protocol handshake

The agent opens one outbound WebSocket to `KUBEMG_BASTION_URL` and holds it,
authenticating the upgrade request with its registration token as a bearer
token — the cluster identity is derived from that token server-side, so an
agent cannot claim to be a cluster it holds no token for.

One socket carries every call, multiplexed by correlation ID, as JSON
frames:

| Frame | Direction | Meaning |
|---|---|---|
| `hello` | agent → bastion | protocol version, agent version, Kubernetes version |
| `welcome` | bastion → agent | which cluster the tunnel bound to |
| `request` | bastion → agent | one HTTP call to replay against the API server |
| `response` | agent → bastion | the API server's answer, or why there isn't one |
| `stream_open` | bastion → agent | open a long-lived call: `exec`, `attach`, `port-forward`, `watch`, `logs -f` |
| `stream_start` | agent → bastion | the response head, or the reason the stream could not open |
| `stream_data` | both ways | one chunk, verbatim — for `exec`/`port-forward` these are the channel-prefixed bytes Kubernetes itself multiplexes stdin/stdout/stderr with |
| `stream_close` | both ways | the stream ended, and why |

`ProtocolVersion` (currently `2`, added with the stream frames) has to match
on both sides — the bastion refuses a mismatched `hello` at the handshake
with "agent speaks an unsupported tunnel protocol version" rather than
letting a stale agent limp along on a wire shape it does not fully
implement. Only the handshake itself is time-bounded (15s); once open, a
call-response round trip is bounded by nothing here and a stream may run for
hours. `agent/internal/protocol` mirrors the bastion's
`pkg/bastion/protocol.go` field-for-field — they are two separate files
because the agent is a separate module, and bumping `ProtocolVersion` is a
breaking change that has to move both together.

`port-forward` rides the WebSocket shape Kubernetes itself offers
(`v2.portforward.k8s.io`), which is channel-prefixed bytes this framing
already carries unchanged — no wire change was needed to support it. A
SPDY-only client is refused with an HTTP `501` naming the fix
(`KUBECTL_PORT_FORWARD_WEBSOCKETS=true`, the default from kubectl 1.31)
rather than left hanging on an upgrade nobody will answer.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `KUBEMG_BASTION_URL` | *(required)* | Public URL of the kubemg server |
| `KUBEMG_CLUSTER_TOKEN` | *(required)* | This cluster's registration token |
| `KUBEMG_KUBERNETES_URL` | `https://kubernetes.default.svc` | The cluster's own API server address |
| `KUBEMG_LISTEN_ADDR` | `:8081` | Health and readiness probe listener |
| `KUBEMG_INSECURE_SKIP_VERIFY` | `false` | Skip **API server** certificate verification — development only |
| `KUBEMG_BASTION_CA` | *(empty)* | PEM chain to trust for the **bastion's** own TLS — set by the install manifest when the bastion is self-signed or behind an internal CA |
| `KUBEMG_BASTION_INSECURE_SKIP_VERIFY` | `false` | Skip **bastion** certificate verification — hand-running against a dev bastion only; logs a warning |

`KUBEMG_KUBERNETES_URL`, `KUBEMG_LISTEN_ADDR` and
`KUBEMG_INSECURE_SKIP_VERIFY` are also available as flags
(`--kubernetes-url`, `--listen`, `--insecure-skip-verify`); a flag wins where
both are set.

`/healthz` reports on the process only — restarting the pod would not fix
an unreachable bastion, so a down tunnel must never fail liveness. `/readyz`
reports on the tunnel (`client.Connected()`), so a pod that cannot reach the
bastion shows as **not ready** in the cluster's own `kubectl get pods` as
well as in kubemg.

## Building from source

Everything runs in containers — no host Go toolchain is required. From the
repository root:

```bash
make agent-test     # go test ./...
make agent-vet      # go vet ./...
make agent-build    # static CGO_ENABLED=0 binary
make agent-image    # distroless image for the local platform, loaded locally
make agent-image-check   # build the image for every published platform, no output pushed
```

## Running it outside Kubernetes, for a test

The agent only needs the four required/optional environment variables above
and a reachable bastion and API server — it does not need to run as a pod
to prove the tunnel works:

```bash
export KUBEMG_BASTION_URL=https://bastion.example.com
export KUBEMG_CLUSTER_TOKEN=<the registration token from the wizard>
export KUBEMG_KUBERNETES_URL=https://<some-api-server>:6443
./kubemg-agent
```

Watch `/readyz` on `:8081` (`curl localhost:8081/readyz`) to confirm the
tunnel came up, or tail the JSON logs — see below.

## Image platforms

`agent/Dockerfile`'s build stage is pinned to the **builder's** own
platform (`--platform=$BUILDPLATFORM`) and cross-compiles to the target
(`TARGETOS`/`TARGETARCH`, supplied by `buildx`) rather than being emulated —
the agent is pure Go with CGO off, so a cross-build produces the same binary
a native one would, and building under QEMU would cost minutes per
architecture for nothing. The published image carries both **amd64** and
**arm64** manifests.

## The manifests live in two places

`deploy/kustomize/base/*.yaml` is the human-facing copy an operator reads or
adapts for GitOps; `backend/pkg/agentpkg/base/*.yaml` is the copy embedded
into the bastion binary via `go:embed` and served from the install wizard
(`GET /install/:token/{agent.yaml,kustomize.tar.gz}`). They exist twice on
purpose — one has to be readable outside a running kubemg install, the other
has to travel inside the binary — and `make manifest-check` (part of `make
verify`) diffs them so the two cannot silently drift. Edit both, or neither.

## Log lines worth watching

The agent logs structured JSON to stdout (`slog.NewJSONHandler`). Worth
alerting on or grepping for:

- `"agent handshake failed"` — the bastion rejected the `hello`; usually a
  protocol version mismatch or a bad/reused registration token. See
  [Troubleshooting](troubleshooting.md).
- `"agent stopped"` with a non-empty `error` — the process is exiting
  (`os.Exit(1)`); a `context.Canceled` exit is a normal shutdown and is not
  logged as an error.
- `"cluster API certificate verification is disabled"` — printed once at
  startup when `KUBEMG_INSECURE_SKIP_VERIFY=true`; the connection to the
  cluster's own API server can be intercepted.
- `"kubemg agent starting"` — carries `version`, `namespace`,
  `kubernetes_url`; the one line that confirms which build and which API
  server a given pod is talking to.

## Licence

Apache-2.0 — see `agent/LICENSE`. This is the permissive half of the
repository by design: it is the only component that runs inside a customer's
cluster, so it has to be readable, buildable from source, and vendorable
without the server's own (AGPL) licence reaching anyone's infrastructure.
