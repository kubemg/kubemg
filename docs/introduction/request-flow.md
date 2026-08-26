# How a request flows

Every read the console does takes the same path a hand-typed `kubectl` call
does — the UI gets no privileged shortcut. This page traces three shapes of
call end to end: a plain proxied request, a streaming call, and a console
read served from the cache.

## A plain call: `kubectl get pods`

A kubeconfig generated for an agent-mode cluster points at kubemg's own proxy
(`{public URL}/api/v1/clusters/:id/proxy`) and carries a kubemg-issued JWT
scoped to that one cluster's proxy route
(`auth.ScopeProxy`) rather than a cluster-native token — see
[Kubeconfigs](../access/kubeconfigs.md). `kubectl` never learns the
difference; it just POSTs to a server URL.

```text
developer's kubectl                kubemg bastion                        kubemg-agent            kube-apiserver
        |                                |                                     |                       |
        |--- GET /api/v1/clusters/3 ---->|                                     |                       |
        |    /proxy/api/v1/namespaces/   |                                     |                       |
        |    payments/pods               |                                     |                       |
        |    Authorization: Bearer <JWT> |                                     |                       |
        |                                | 1. RequireAuth: parse JWT,          |                       |
        |                                |    scope=proxy confines the token   |                       |
        |                                |    to this exact route (isProxyRequest)|                    |
        |                                | 2. re-read user + effective grant   |                       |
        |                                |    (AccessForUser) -- not cached     |                      |
        |                                | 3. allowedNamespace: "payments" is   |                       |
        |                                |    inside the grant's scope? OK      |                       |
        |                                | 4. guardrail check                  |                       |
        |                                | 5. strip client Authorization and   |                       |
        |                                |    Impersonate-* headers            |                       |
        |                                | 6. set Impersonate-User/-Group      |                       |
        |                                |    from the resolved k8s_role       |                       |
        |                                | 7. audit record written (async)     |                       |
        |                                |------- MessageRequest, over -------->|                       |
        |                                |        the existing tunnel          |                       |
        |                                |        (correlation ID)             |                       |
        |                                |                                     |--- Impersonate-User,-|
        |                                |                                     |    Impersonate-Group ->|
        |                                |                                     |<---- RBAC decides -----|
        |                                |<------ MessageResponse -------------|                       |
        |<---- pods JSON -----------------|                                     |                       |
```

The steps that matter:

1. **`RequireAuth`** validates the bearer token. A `ScopeProxy` token is
   confined to exactly `/api/v1/clusters/:id/proxy/*path` for the cluster ID
   baked into its claims (`isProxyRequest` matches the *registered route*,
   `c.FullPath()`, rather than the raw URL) — a kubeconfig on a laptop cannot
   be replayed against any other endpoint.
2. **The grant is re-read on every call.** `AccessForUser` is not consulted
   once at login; it runs again for this request, merging direct and
   group-inherited access and dropping anything expired. This is what makes
   revocation and JIT expiry take effect on the *next* call rather than at
   the token's own expiry.
3. **Namespace scope is enforced here, not by Kubernetes.** Impersonation
   groups have no way to express "only these three namespaces," so
   `allowedNamespace` checks it locally: an unscoped grant passes, a scoped
   grant is checked against the namespace the URL or body names, and
   discovery paths are allowed through regardless. See
   [The access model](../access/model.md).
4. **Command guardrails** get a look before anything is forwarded — the one
   refusal kubemg makes on its own authority, because a guardrail stops a
   call the caller is otherwise fully entitled to make. See
   [Command guardrails](../access/guardrails.md).
5. **Client-supplied `Authorization` and `Impersonate-*` headers are
   stripped** before kubemg's own are set, so nothing a caller sends can
   widen what it is impersonated as.
6. **Impersonation, not a stored credential.** The proxy sets
   `Impersonate-User` to the caller's own username and `Impersonate-Group` to
   `kubemg:<role>` plus `kubemg:users`. The agent forwards the call to the
   local API server exactly as received; **the cluster's own RBAC makes the
   authorization decision**, not kubemg.
7. **The audit record is written asynchronously** and never blocks the
   response — a slow database must never become a slow `kubectl`.

A refusal at any of steps 1–4 is audited exactly as loudly as a success; it
never reaches the tunnel at all, so the agent and the target API server never
see it.

## A streaming call: `kubectl logs -f` or `kubectl exec`

A long-lived call cannot be expressed as one request/response pair, so the
tunnel carries it as a separate stream, correlated by ID, multiplexed over
the same socket as everything else.

```text
developer's kubectl          kubemg bastion                                 kubemg-agent      pod
        |                          |                                              |            |
        |--- exec (upgrade) ------>|                                              |            |
        |                          | same steps 1-6 as above, then:               |            |
        |                          | audit record, phase=open  (session visible   |            |
        |                          |   while it is still running)                 |            |
        |                          | mint SessionID                               |            |
        |                          | begin recording tee (if enabled)             |            |
        |                          |----- MessageStreamOpen -------------------->|            |
        |                          |<---- MessageStreamStart ---------------------|            |
        |                          |                                              |--- exec -->|
        |<==== bytes both ways, MessageStreamData, tee'd into the recording =====>|<== I/O ===>|
        |                          |                                              |            |
        |--- session ends -------->|                                              |            |
        |                          | audit record, phase=close                    |            |
        |                          |   (duration, bytes each way)                 |            |
        |                          | recording finalised                          |            |
```

Two things a plain call does not have:

- **Two audit records, not one.** A `PhaseOpen` record is written the moment
  the stream is accepted, and a `PhaseClose` record on the way out carries
  duration and bytes transferred — an hour-long shell is visible in the trail
  while it is still open, not only once it ends. `VerbFor` names the
  subresource directly (`exec`, `attach`, `portforward`) rather than the HTTP
  method, so a shell in a production pod never reads as a generic `get`.
- **A recording tee.** `exec` and `attach` are additionally teed into a
  gzipped asciinema v2 cast under a `SessionID` minted at stream open — the
  same correlation key both audit records carry. This runs beside the
  bridged bytes, never blocking them: a disk that stops accepting writes
  ends the recording and leaves the shell running. See
  [Session recording](../audit/session-recording.md).

`port-forward` follows the same `stream_open`/`stream_data`/`stream_close`
shape but is not recorded — arbitrary TCP has no terminal to play back. A
client that only speaks the older SPDY transport is refused outright with a
`501` naming the fix, rather than left hanging on an upgrade nobody answers.

## A console read: cached and live

The browser makes the same proxied, impersonated, audited call the two
sections above describe — but a console is a UI that re-asks the same
question far more often than a `kubectl` script does, so two more layers sit
in front of the tunnel round trip.

```text
browser                    kubemg bastion (cachedRead middleware)              tunnel + agent
   |                              |                                                  |
   |--- GET .../resources/pods -->|                                                  |
   |                              | key = hash(v1, clusterID, userID, role,          |
   |                              |            path, sorted query)                  |
   |                              |                                                  |
   |                       [cache hit, <5s old, 200 last time]                       |
   |<---- served from memory, no tunnel round trip, no new audit record -------------|
   |                              |                                                  |
   |                       [cache miss, or Cache-Control: no-cache from Refresh]     |
   |                              |------------------------------------------------->|
   |                              |         (the full flow above)                    |
   |<---- fresh answer, cached for the next few seconds --------------------------- -|
```

- **The 5-second read cache** (`cachedRead`, default TTL) sits in front of
  every `/resources`, `/metrics` and metrics-query route. The key includes
  the caller's identity, so a cache entry is never served across users; only
  a `200` is ever stored, so a `403` never outlives the grant that produced
  it; and any non-GET on the same cluster drops the whole cluster's cached
  entries, so a scale or restart shows up on the very next list rather than
  five seconds later.
- **Live ticks** re-run the same cached read every 10–15 seconds while a tab
  is actually visible and attended, so an Explore list or the fleet overview
  updates on its own. A tick is invisible on success — no skeleton, no
  re-render if the answer is unchanged — and on failure it leaves the last
  good answer on screen rather than replacing it with an error. A tick
  deliberately does **not** send `Cache-Control: no-cache`, so several open
  tabs of the same console can collapse onto one shared cache entry and one
  tunnel round trip; the **Refresh** button is what actually forces a fresh
  read.

Both layers only ever change *when* a call happens, never *whether* it is
authorized — every cached or live-ticked read is still the same impersonated,
grant-checked, audited call underneath. See
[Read caching and live reads](../clusters/explore.md) for the operator-facing
side of this.
