# Terminals and logs

Every shell and log view in the console lives here: pod logs, the pod terminal, debug containers, port-forward and the browser `kubectl` shell. All of them ride the tunnel like a resource read, with the same impersonation, namespace scope, guardrails and audit trail.

Streaming calls (a followed log, a shell, a port-forward) are recorded **twice**, at open and at close, so a long session is visible while it is still running.

## Pod logs

The pod drawer's **Logs & Terminal** tab reads one container's log.

- The snapshot returns the last lines (default 200, any value 1–5000), with timestamps.
- **Follow** streams live. Stopping it ends cleanly, not as a failure.
- **Filter** and **wrap** only change the view. Lines that scroll past while you filter are kept, up to a buffer of roughly 400 KB.

## Pooled workload logs

A workload's **Logs** tab tails all its pods together.

- It works for Deployments, StatefulSets, DaemonSets, Jobs and ReplicaSets. A CronJob owns Jobs, not pods, so it has no pooled view.
- Pods are chosen from the workload's own selector, never from the caller. A selector that is empty or cannot be rendered safely is refused (`409`), not widened.
- At most **8** pods are followed at once, each with its own colour toggle.
- Lines are ordered by Kubernetes' timestamp, not by arrival.
- The filter matches pod names as well as text. One pod ending is not a failure; follow switches off when the last stream ends.
- Eight followed pods write eight ordinary `log` audit records.

## The in-browser terminal

The pod terminal runs `exec` (a new process) or `attach` (the running one). A **bash/sh** picker is offered, since not every image has both.

A browser cannot set headers on a WebSocket, so the console exchanges your session for a **one-time ticket** (single use, twenty seconds) and puts that in the URL. It is accepted only on an upgrade request and is stripped before the cluster or audit trail sees it. It works behind a load balancer without session affinity.

**A shell is recorded.** Every `exec`/`attach` is saved as a gzipped [asciinema](https://asciinema.org) cast, encrypted at rest when a recording key is configured, with keystrokes optionally excluded. A line above the terminal always says what is captured and whether it is encrypted. See [Session recording](../audit/session-recording.md).

### How much one message may carry

A message into a session (`exec`, `attach`, port-forward, the terminal) may carry at most **1 MiB**. `kubectl` sends at most 32 KiB, so nothing ordinary comes near. A larger one is refused with WebSocket close code `1009`, and the closing audit record says why.

Agents from before this limit silently cut messages at 4 KiB. A `kubectl cp` that leaves a short file, or stdin piped into `kubectl exec -i` that arrives truncated, means the agent is old: re-apply the cluster's install package.

## Debugging a pod with no shell

Distroless and scratch images have no shell to `exec` into. The **Debug** action beside the terminal adds a throwaway container to the running pod, sharing a chosen container's process namespace, and opens a terminal in it.

- The terminal opens once the debug container reports running. If it never starts, you see the cluster's reason (such as an image that will not pull).
- It starts `sh`; the picker can switch it.
- The sheet states two limits before the button is reachable:
    - **It cannot be undone.** Kubernetes cannot delete an ephemeral container, so it stays for the pod's life.
    - **It shares namespaces.** It sees the target's processes and network. That is the point, and also a privilege.
- A concurrent change to the pod surfaces as the cluster's `409`, and a namespace outside your grant is refused first.
- The debug image is the `debug_image` setting (**Settings**; `KUBEMG_DEBUG_IMAGE` at boot). Air-gapped sites point it at their own mirror.

## Port-forward through the proxy

Only the WebSocket transport of port-forward is proxied. A client that negotiates the legacy SPDY transport is refused with `501`:

```
run kubectl with KUBECTL_PORT_FORWARD_WEBSOCKETS=true (default on Kubernetes
1.31 and later). The SPDY transport is not proxied.
```

Kubernetes 1.31 and later default to WebSocket, so this appears on older clients or when the flag is set to `false`:

```bash
KUBECTL_PORT_FORWARD_WEBSOCKETS=true kubectl port-forward pod/my-pod 8080:80
```

Port-forward carries arbitrary TCP, not a terminal, so it is **not** recorded as a session.

## The browser shell

The pod terminal answers what is wrong with **one pod**. The browser shell is for everything else: `kubectl get` across a namespace, a `kubectl diff`, a `describe` of something no form covers. It is a terminal in the console with `kubectl` on the path, one per person per cluster.

### Opening one

Press **Shell** in the header, on any page of a cluster whose agent is attached. A dock opens along the bottom and a session starts at once.

The dock sits over the console, not on a page, so it keeps running while you navigate. Closing it (`×`) hides it and leaves the session running until its idle window ends. Ending it (power icon) deletes the pod now and withdraws the credential inside it.

### What it is

A pod kubemg creates on the target cluster when someone asks, running kubemg's own image (busybox and `kubectl` on a distroless base, **no package manager**).

- It **holds no cluster credential**: no service account token is mounted, and its account is granted nothing.
- Once it is up, kubemg writes a kubeconfig into its home directory. That kubeconfig points at **kubemg's own proxy** and carries a token scoped to you.

So every command is impersonated as you, answered by the cluster's own RBAC, held to your namespace scope and audited, exactly like `kubectl` on a laptop.

!!! note "It is not a way around the tunnel"
    A `view` grant opens a shell that can read. An `edit` grant opens one that can write what `edit` can write. Being inside the cluster changes nothing.

### Who may open one

Anyone granted the cluster. It is not admin-only, so a read-only user keeps the one place they could run `kubectl describe`.

### How long it lives

| Clock | Default | Enforced by |
| --- | --- | --- |
| Idle | 1 hour without a keystroke | kubemg's reaper, every 2 minutes |
| Absolute | 8 hours | the pod's own deadline in the cluster |

The absolute deadline is written into the cluster, so a bastion that is down does not leave a forgotten shell running. The lifetime is capped by the [kubeconfig ceiling](../access/kubeconfigs.md), so a shell never outlives its credential.

**Nothing written in a shell survives it.** The root filesystem is read-only; the only writable paths are two 64 MiB scratch mounts (`/home/shell` and `/tmp`).

### What bounds it

The pod runs non-root (uid 65532) with all capabilities dropped, a read-only root filesystem, privilege escalation off, the `RuntimeDefault` seccomp profile, no host network, PID namespace or host paths, `restartPolicy: Never`, and limits of 500m CPU and 256Mi memory.

### Recording and audit

Attaching is an `exec` through the proxy, so it is recorded on the same terms as the pod terminal, [guardrails](../access/guardrails.md) inspect each line you type, and two audit records are written. The recording notice appears before the first keystroke.

The audit row names both identities: **user** is you, and **impersonated user** is `kubemg:shell-runner` for the pod's lifecycle calls (create, seed, stamp, delete).

The credential written into the shell is listed under **You → My access**, marked as a shell. Revoking it stops the shell's `kubectl` on its next call; the terminal stays but can no longer reach the cluster.

### What it does not have: `helm`

`helm` was removed from the image because its upstream binaries carry known critical/high Go standard library findings that no version bump fixes. Installing, upgrading, editing values, history, rollback and uninstall are in the console instead: see [Helm](helm.md).

??? info "What left with the binary"
    The console [deliberately does not do](helm.md#honest-limits) these, so they left with `helm`:

    - `oci://` charts (repositories are `http(s)` only)
    - `helm get manifest`, `helm template`, `helm diff` (a release's rendered manifest is never returned to a client)
    - `helm test` and hook waiting (`--wait`/`--atomic`)
    - `helm lint` / `helm show` on a chart whose repository is not registered

    For these, run helm from a workstation against a [generated kubeconfig](../access/kubeconfigs.md), which is the same proxied, audited path.

### Requirements

- The cluster must be in **agent mode**. A direct-mode cluster is refused, with the reason named.
- Pods must be able to reach `KUBEMG_PUBLIC_URL`, the same egress the agent uses.
- The agent's manifests must be current (the `kubemg-shell` service account and `kubemg-shell-runner` Role). **An existing install picks these up only by re-applying its manifests.**

### Operator settings

Under **Admin → Agent settings**:

| Setting | Environment | Default |
| --- | --- | --- |
| Offer a browser shell | `KUBEMG_SHELL_ENABLED` | on |
| Shell image | `KUBEMG_SHELL_IMAGE` | `ghcr.io/kubemg/kubemg-shell:<version>` |
| Idle timeout | — | 60 minutes |
| Maximum lifetime | — | 8 hours |

Turning the shell off refuses **new** shells and leaves running ones alone. A server started without a shell image cannot be made to offer one from the database.

For an air-gapped site, mirror the shell image next to the agent's and point `KUBEMG_SHELL_IMAGE` at it. The shell pod pulls with the agent's [image pull secret](../install/air-gapped.md#a-mirror-that-requires-authentication) when one is configured.

## What lands in the audit trail

Every call is recorded, refusals included, under a verb that names what happened: `exec`, `attach`, `log`, `portforward`. A streaming call writes two records, at open and at close (with duration and bytes each way). See [The audit trail](../audit/trail.md) and [Session recording](../audit/session-recording.md).
