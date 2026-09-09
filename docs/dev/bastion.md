# Tunnel and proxy

`backend/pkg/bastion` is the part of kubemg that makes the whole security model
possible: the cluster dials it, it never dials the cluster, and every Kubernetes
call — console read, `kubectl`, exec, port-forward — leaves through the same
door.

| File | What it owns |
|---|---|
| `server.go` | The tunnel endpoint the agent connects to |
| `registry.go` | Which clusters currently have a live tunnel |
| `proxy.go` | Resolving a request to a tunnel, authorising it, forwarding it |
| `stream.go` | Multiplexing long-lived streams over one connection |
| `exec.go` | Exec and attach, and the recording tee |
| `shell.go` | The browser shell's session |
| `audit.go` | The record every call produces |
| `protocol.go` | The wire format, mirrored in the agent module |
| `token.go` | Registration tokens |

## The handshake

`GET /agent/v1/tunnel` sits **outside** the JWT middleware — the agent
authenticates with its registration token, which is the only credential kubemg
holds for an agent-mode cluster. The install package routes
(`GET /install/:token/agent.yaml` and `.../kustomize.tar.gz`) are unauthenticated
for the same reason: the token *is* the credential.

`ProtocolVersion` is checked at the handshake and a mismatch is refused. Bumping
it is a breaking change that requires every agent to be upgraded, so it is a
decision rather than a bump.

## Proxying a request

`ANY /api/v1/clusters/:id/proxy/*path` is the entire `kubectl` server surface.
Every verb lands there. `Proxy.resolve` runs the checks in a fixed order, and the
**first** of them is the revocation snapshot.

Three things the proxy does itself, and one it deliberately does not:

- **Namespace scope** is enforced here. A path outside the caller's granted
  namespaces is refused before it reaches the cluster.
- **Guardrails** are the one refusal kubemg makes on its own initiative.
- **Impersonation headers** are set from the caller's identity, so the cluster's
  RBAC answers for the person.
- **Role is not enforced locally.** What a role may do is the cluster's own RBAC
  to decide, and duplicating that here would create two answers that can
  disagree.

`Proxy.Call` sends `application/merge-patch+json` on a PATCH. `application/json`
is a 415, and because the patchers are best-effort it failed silently — which is
exactly the kind of bug the content-type helper exists to prevent.

## Revocation

An agent-mode kubeconfig carries a proxy-scoped JWT with a `jti`. Revoking it
means adding that `jti` to a **published immutable snapshot** (`pkg/credentials`)
that the proxy reads lock-free as its first check, answering 401.

The snapshot is republished *before* the revoke call answers, and a refresher
picks the change up on sibling replicas within thirty seconds. **An unreadable
register fails open on nothing**: a failed refresh keeps the previous snapshot,
never empties it and never inverts it. Only live agent-mode revocations are in
it, because direct mode cannot be revoked this way at all — a direct-mode
kubeconfig is backed by a service account in the cluster, so revoking is
all-or-nothing per cluster and is reported as such rather than silently
half-done.

**Never report a revoke that did not happen.** A revoke that could not land
carries its reason as the audit record's error.

## Streaming

Protocol v2 multiplexes streams over the one tunnel connection by correlation ID.
Two shapes:

- `serveBodyStream` for a watch or `logs -f` — a response body that never ends.
- `serveUpgradeStream` for exec, attach, port-forward and the browser shell —
  verbatim bytes in both directions.

A backlogged stream is killed **alone**. It must never block the tunnel, because
one slow `logs -f` would otherwise take every other cluster call with it.

`port-forward` rides the upgrade path over WebSocket. SPDY is refused with a 501
that names `KUBECTL_PORT_FORWARD_WEBSOCKETS=true`, so the error tells the
operator the fix rather than just failing.

A browser terminal authenticates with `?access_token=`, accepted **only on an
upgrade request**, and the query is stripped before the request reaches the
cluster or the audit trail.

## Recording

Every exec and attach is teed into an asciinema v2 cast, gzipped, encrypted with
AES-256-GCM in chunks, written `0600` inside a `0700` directory. Never a second
session — the recording rides the stream that already exists.

- The correlation key is the session ID minted at stream open, not an audit row
  id.
- A recorder that fails to start logs an error and turns recording off. It never
  blocks a console from opening.
- A truncated, reordered or altered file **fails to decrypt** rather than
  replaying short.
- Whether a file is encrypted is read from the file's own magic bytes, not from
  the current configuration — otherwise rotating the setting would misread old
  files.
- Watching a recording is itself audited. Reading the index deliberately is not.

`port-forward` is not recorded: there is no terminal to record, only bytes.

## The browser shell

A `kubectl` pod kubemg runs, one per user per cluster, and the interesting part
is what it does **not** hold: the pod has no cluster credential at all,
`automountServiceAccountToken: false` on a service account granted nothing.
Reach arrives as a proxy-scoped kubeconfig for the caller, written over an exec
on **stdin** — never a Secret, never an environment variable, never in the audit
path or a recording.

It is ephemeral twice: an idle sweep behind a lease, and the pod's own
`activeDeadlineSeconds`, which still expires the pod while kubemg is down. The
idle clock is an annotation on the pod rather than a database row, so it cannot
disagree with the thing it describes.

`helm` was in that image and was deliberately removed — its release binaries
carried a Go standard library behind Go's own security releases. The console
installs, upgrades, rolls back and uninstalls through the tunnel instead.
