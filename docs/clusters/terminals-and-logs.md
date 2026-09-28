# Terminals and logs

Everything on this page rides the tunnel exactly like a resource read — the
same impersonation, the same namespace scope, the same guardrails and the
same audit trail. Streaming calls (a followed log, an interactive shell, a
port-forward) are the one category recorded **twice**, at open and at close,
so an hour-long session is visible on the trail while it is still running.

## Pod logs

The pod drawer's **Logs & Terminal** tab reads a single container's log.

- `GET .../resources/pods/:pod/logs` returns the last `tail` lines
  (default 200, any value 1–5000) as a plain-text snapshot, always with
  `timestamps=true` set on the underlying Kubernetes call.
- **Follow** is not this route polled repeatedly — it opens the *streaming*
  proxy path directly (`?follow=true&timestamps=true&tailLines=200`) with a
  raw `fetch`, bypassing axios entirely, because axios buffers a whole
  response body and a follow is exactly the case where that would defeat the
  point. Stopping follow aborts the fetch, which the bastion records as a
  clean end of stream rather than a failure.
- The **filter** and **wrap** controls are a view over an in-browser buffer,
  never a filter sent to the stream — narrowing what is shown must never cost
  the lines that scrolled past while narrowed, so widening the filter again
  reveals them without a re-fetch. The buffer is capped (roughly 400 KB of
  text) so a chatty container cannot grow the tab's memory without bound.

## Pooled workload logs

A workload's logs are its pods' logs, tailed together. `GET
.../resources/workload/pods` is a **resolver**, not a new log route — it
turns a Deployment/StatefulSet/DaemonSet/Job/ReplicaSet into the pod names it
owns, and the log itself is still the ordinary per-pod read, once per pod, so
there is no new streaming shape and no new audit verb (eight followed pods
write eight ordinary `log` records, not one pooled one).

- The pod selector is **derived from the workload's own `spec.selector`**,
  never accepted from the caller — a caller-supplied label selector would
  turn this into a general pod query capable of enumerating a namespace by
  label. An empty selector, one this build cannot render, or one containing
  characters that could inject selector syntax is refused outright (`409`)
  rather than silently widened.
- **CronJob** is absent from this resolver: it owns Jobs, not pods, and has
  no `spec.selector` of its own. Its running pods, when there are any, are
  resolved through its Jobs instead — see the CronJob-specific pod list.
- Concurrency is capped at **8** streams, which is the length of the chart
  colour palette (`--chart-1..8`): each followed pod gets its own colour
  swatch as a toggle, and a ninth pod would either repeat a colour or leave
  one untagged.
- Lines from every followed pod are ordered by the **timestamp** Kubernetes
  puts on each one (`timestamps=true`), not by network arrival order —
  without that, interleaving eight independent streams would draw whichever
  chunk the browser happened to receive first as if it were the earliest.
- The pooled view's filter matches the pod name as well as the line text, so
  narrowing to one pod stops nothing — the other streams keep running behind
  the filter, exactly like the per-pod filter above.
- A clean end of stream on one pod (it finished, or was rescheduled) is not
  treated as a failure; only when the **last** open stream ends does the
  follow toggle flip back off.

## The in-browser terminal

`exec` (running a new process) and `attach` (joining the one already
running) both ride `serveUpgradeStream`: the browser opens a WebSocket to the
bastion, which bridges it to the agent's own upgraded session over the
tunnel, piping bytes verbatim — the Kubernetes exec/attach channel protocol
already multiplexes stdin/stdout/stderr/resize inside those bytes, so the
bastion does not have to understand them to carry them.

Because a browser cannot set a header on a WebSocket handshake, the terminal
authenticates with a credential in the URL instead of an `Authorization`
header. That credential is never your session: the console first exchanges
the session for a **one-time ticket**, good for twenty seconds and a single
use, so a copy left in a proxy or load-balancer access log opens nothing. The
ticket is accepted **only** on an upgrade request and is stripped from the URL
before it reaches the audit trail or the cluster. Tickets are kept in the
database, so with several replicas behind a load balancer the upgrade may land
on any of them — no session affinity is needed.

**A shell is recorded.** Every `exec`/`attach` through the proxy is teed into
a gzipped [asciinema v2](https://asciinema.org) cast as it happens — not a
second session, a tee of the bytes the bridge is already carrying — encrypted
at rest by default whenever a recording key is configured, with keystrokes
optionally excluded. `PodTerminal` states this as a **persistent line above
the terminal**, not a dialog that gets dismissed and forgotten: whether
output alone or output-and-keystrokes is captured, and whether the result is
encrypted at rest. See [Session recording](../audit/session-recording.md) for
what is actually stored, how it is encrypted, and who may watch someone
else's.

A shell selector (`bash`/`sh`) is offered on the pod terminal, since not
every image ships both.

A terminal that is not *inside* one particular pod — `kubectl` in a pod kubemg
runs for you — is [the browser shell](browser-shell.md). It rides this same
bridge and is recorded on the same terms.

## Debugging a pod with no shell

`exec` needs a shell inside the target container, and a distroless or
scratch image has none — which is exactly the kind of image worth running in
production, and therefore exactly the pod an operator most needs to get
into. The **Debug** action beside the terminal is `kubectl debug`'s trick:
`POST .../resources/pods/debug` writes a second, throwaway container onto
the running pod (the pod's own `ephemeralcontainers` subresource), sharing
the process namespace of whichever existing container is chosen, and the
console then execs into *that* container through the same
`serveUpgradeStream` bridge above — no new streaming path, no new
permission.

The write landing is not the same moment as the container being attachable:
the image still has to be pulled and the container still has to start, and an
exec attempted before either finishes fails with an opaque "waiting to
start". So the console polls the pod's own status after the write and opens
the terminal only once the debug container reports running, showing the wait
as plain text rather than a spinner — and, if it never starts, the reason the
cluster gave (an image still pulling, or one that never will) rather than a
terminal that just fails. The debug session opens with `sh`, since a minimal
debug image commonly has no `bash`; the shell picker can still switch it.

Two things about it do not follow the rules everything else on this page
does, and the sheet says both before the button is reachable:

- **It cannot be undone.** The Kubernetes API has no delete for an ephemeral
  container — once it is added, it stays on the pod for the pod's life.
- **It shares namespaces.** The debug container sees the target container's
  process (and, from there, its network) namespace, which is the point and
  also a privilege.

The write is the same read-modify-write every workload action here uses: the
pod is read first, so a concurrent change surfaces as the cluster's own
`409` rather than a blind overwrite, and a namespace outside the caller's
grant is refused before the cluster is ever asked. The image the container
runs is an operator setting (`debug_image` on the Settings page,
`KUBEMG_DEBUG_IMAGE` at boot) rather than a build constant — an air-gapped
site has no path to a public registry and points this at its own mirror the
same way it does the browser shell's image.

## Port-forward through the proxy

`port-forward` rides the same `serveUpgradeStream` bridge as `exec`/`attach`.
Kubernetes offers port-forward over two transports — legacy SPDY, and a
WebSocket subprotocol (`v2.portforward.k8s.io`) — and only the WebSocket
shape is proxied here, because it is channel-prefixed bytes the existing
bridge already carries verbatim; carrying SPDY as well would mean
implementing a second multiplexing protocol inside the tunnel for a
transport Kubernetes itself is retiring.

A client that negotiates SPDY is refused outright with an honest `501`
rather than left hanging on an upgrade nobody will answer:

```
run kubectl with KUBECTL_PORT_FORWARD_WEBSOCKETS=true (default on Kubernetes
1.31 and later). The SPDY transport is not proxied.
```

`kubectl port-forward` against a 1.31+ cluster already defaults to the
WebSocket transport, so most operators never see this — it surfaces on an
older client or an explicit `KUBECTL_PORT_FORWARD_WEBSOCKETS=false`. Setting
the flag explicitly is the fix:

```bash
KUBECTL_PORT_FORWARD_WEBSOCKETS=true kubectl port-forward pod/my-pod 8080:80
```

Port-forward carries **arbitrary TCP**, not a terminal, so it is deliberately
**not** teed into a session recording the way `exec`/`attach` are — there is
no terminal content to capture, only a byte stream kubemg cannot interpret.

## What lands in the audit trail

Every one of these calls is recorded, refusals included, and verbs are named
after what actually happened rather than the HTTP method underneath it —
`VerbFor` names a shell `exec` or `attach`, a followed log `log`, and
port-forward `portforward`, specifically so a shell opened against a
production pod reads as a shell in the trail rather than being buried under
the far more common `get`. A streaming call writes **two** records — one at
open, one at close carrying duration and bytes transferred each way — so a
session that is still running is already visible, not only after it ends.
See [The audit trail](../audit/trail.md) for how to query these, and
[Session recording](../audit/session-recording.md) for replaying an `exec`
or `attach` from its audit row or from the sessions index.
