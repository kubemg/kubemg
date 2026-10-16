# The agent module

`agent/` is a **separate Go module** under Apache-2.0, and the only part of
kubemg that runs inside somebody else's cluster. Its job is one sentence: hold an
outbound tunnel to the bastion and replay whatever arrives down it against the
cluster's own API server.

What it is, what it refuses to do, its environment variables and the log lines
worth watching are in the user guide's
[The agent](../clusters/agent.md). This page is the half that
only matters if you are changing it.

## Why it is a module of its own

Its `go.mod` depends on exactly one third-party package,
`github.com/gorilla/websocket`. No client-go. That is what keeps the static
binary near 10 MB and its dependency surface auditable at a glance, and it is
why the agent cannot simply import a helper from `backend/` — the two are
separate modules under separate licences, and moving code across that line
changes the licence it ships under.

Adding a dependency here is a decision to raise in review, not a detail.

## The tunnel and the protocol handshake

The agent opens one outbound WebSocket to `KUBEMG_BASTION_URL` and holds it,
authenticating the upgrade request with its registration token as a bearer
token — the cluster identity is derived from that token server-side, so an
agent cannot claim to be a cluster it holds no token for.

One socket carries every call, multiplexed by correlation ID, as JSON
frames:

| Frame | Direction | Meaning |
|---|---|---|
| `hello` | agent → bastion | protocol version, agent version, Kubernetes version, capabilities offered |
| `welcome` | bastion → agent | which cluster the tunnel bound to, and the capabilities accepted |
| `request` | bastion → agent | one HTTP call to replay against the API server |
| `response_part` | agent → bastion | a slice (≤256 KB) of a response body, ahead of the `response` that ends it — only when `response-parts` was negotiated |
| `response` | agent → bastion | the API server's answer, or why there isn't one; after parts, the status, headers and the last slice |
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

**Capabilities are how the wire grows without a bump.** The agent lists what it
can do in `hello.capabilities`; the bastion answers with the subset it accepts
in `welcome.capabilities` (`negotiateCapabilities`), and each side uses only
what both named. A side that names nothing gets the v2 behaviour exactly, so an
agent and a bastion can be upgraded in either order. The one capability today
is `response-parts`: a response body leaves the agent as `ResponsePartSize`
(256 KB) slices read straight off the API server's answer, and the bastion joins
them under the same ceiling a whole response always had (`maxResponseBody` =
one 16 MB frame; the agent's own cap stays 8 MB, counted in bytes as the API
server sent them — compressed, when the client asked for gzip, as kubectl and
the console's own reads do). A final `response` carrying
`error` discards the parts before it — half a list is never presented as a
whole one.

**Two outbound queues on each side.** Stream frames (`stream_*`) go in an
*urgent* queue; `request`, `response` and `response_part` go in a *bulk* one.
The single writer drains urgent first and gives bulk a turn every
`urgentBurst` (16) urgent frames, so a terminal's output never waits behind a
list and a busy port-forward cannot starve an API call into its timeout. Before
this, one 8 MB list was one ~11 MB base64 frame at the head of a single FIFO:
every session's frames waited behind it, and on a link under ~10 Mbit/s the
write outlasted the 10s deadline and dropped the tunnel for the whole cluster.
The agent's bulk queue is only `bulkQueue` (8) frames deep and a sender waits
on its own call's deadline (`kube.RequestTimeout`, 55s), which is what keeps
concurrent lists from piling up in the pod.

**The agent bounds itself.** At most `maxConcurrentRequests` (16) proxied calls
run at once; the rest wait for a slot on their own deadline rather than being
refused. Streams are not counted — a watch or a shell holds its slot for hours.
`main.applyMemoryLimit` sets the Go runtime's soft limit to 80% of
`KUBEMG_MEMORY_LIMIT_BYTES`, which the manifest fills from the container's own
`limits.memory`; an explicit `GOMEMLIMIT` wins.

`port-forward` rides the WebSocket shape Kubernetes itself offers
(`v2.portforward.k8s.io`), which is channel-prefixed bytes this framing
already carries unchanged — no wire change was needed to support it. A
SPDY-only client is refused with an HTTP `501` naming the fix
(`KUBECTL_PORT_FORWARD_WEBSOCKETS=true`, the default from kubectl 1.31)
rather than left hanging on an upgrade nobody will answer.

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
(`GET /install/:ticket/{agent.yaml,kustomize.tar.gz}`). They exist twice on
purpose — one has to be readable outside a running kubemg install, the other
has to travel inside the binary — and `make manifest-check` (part of `make
verify`) diffs them so the two cannot silently drift. Edit both, or neither.
