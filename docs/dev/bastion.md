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
(`GET /install/:ticket/agent.yaml` and `.../kustomize.tar.gz`) are
unauthenticated too, but the path carries a **single-use download ticket**,
never the registration token (`pkg/api/agent_install.go`). The ticket is the
`ws_tickets` pattern: 256 bits, `kmgi_`-prefixed, only its SHA-256 stored in
`agent_install_tickets`, redeemed by one `DELETE … RETURNING` so any replica
can serve it and exactly one fetch wins. It is minted on every JSON read of
`GET /api/v1/clusters/:id/kustomize`, lives `installTicketTTL` (15 min), and
both URLs share it — whichever form is fetched first spends it. A path segment
with the tunnel token's `kmg_` prefix is refused `410` **by shape, without a
lookup**, so a pre-ticket URL is dead whether or not its token is live. A store
error on redeem refuses.

**Rotation** (`POST /api/v1/clusters/:id/agent-token/rotate`, admin): the new
token is written and the cluster's outstanding tickets deleted in one
transaction (`db.RotateClusterAgentToken`) — a ticket renders the *current*
token at fetch time, so a leaked pre-rotation URL would otherwise hand out the
new one. Then `Server.Retire` closes the local tunnel with a close frame naming
the reason (`ErrCredentialRetired`); a tunnel held by another replica is closed
by `RunCredentialSweep` (30s), which re-resolves every live tunnel's token.
**The sweep fails open on a store error** — a database blip must not drop the
fleet, and the handshake still refuses the old token. No grace window. The
agent reads its Secret as env vars, fixed at container start, so the pod
template carries `kubemg.io/secret-checksum` (`agentpkg.secretChecksum`, a
truncated SHA-256 of URL+token+CA): without it a re-apply after rotation
updated the Secret and left the pod presenting the old token forever — found
by the e2e pass, not by a test.

**Displacement** (`Registry.Add` is newest-wins) writes `agent-displaced` via
`Server.UseAuditor`: user `kubemg:agent`, source address on the context, and
both agents' versions, both connection times and the previous address in the
path's query (the audit row has no free-text column, and the path is what the
table, the SIEM forward and an alarm all carry). A rolling Deployment
displaces too, and is recorded identically — nothing can tell the two apart
that an impostor cannot imitate. Both agent verbs are on `auditpolicy`'s
unsuppressible floor and in the alarm verb vocabulary.

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

A browser terminal authenticates with `?access_token=<ticket>`, accepted **only
on an upgrade request**, and the query is stripped before the request reaches
the cluster or the audit trail. The value is never the session JWT: the console
mints a ticket with `POST /auth/ws-ticket` just before opening the socket —
32 random bytes, single-use, twenty seconds (`pkg/auth/wsticket.go`). Tickets
live in the `ws_tickets` table, keyed on their SHA-256, and are redeemed with one
`DELETE … RETURNING`: the mint and the upgrade are two requests a load balancer
owes no affinity, so a ticket held in process memory was refused by every
replica but the one that minted it. A store that cannot be read refuses the
upgrade.

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
