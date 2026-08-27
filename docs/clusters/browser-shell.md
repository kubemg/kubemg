# The browser shell

The pod terminal answers what is wrong with **one pod**. The browser shell
answers everything else: `kubectl get` across a namespace, a manifest diff,
`helm upgrade` with a chart. It is a terminal in the console with `kubectl`
and `helm` on the path, one per person per cluster.

It exists so that the moment a question outgrows a form, the answer still
happens **inside** kubemg rather than in a terminal nobody can see.

## What it actually is

A pod, on the target cluster, that kubemg creates when somebody asks for one:

* it runs kubemg's own image — busybox, `kubectl` and `helm`, on a distroless
  base, **with no package manager in it**;
* it **holds no cluster credential**. No service account token is mounted,
  and the account it runs as is granted nothing anywhere;
* once it is up, kubemg writes a kubeconfig into its home directory over an
  exec. That kubeconfig points at **kubemg's own proxy**, not at the cluster's
  API server, and carries a token scoped to the caller.

So every command typed in the shell is a call to kubemg: impersonated as the
person who opened it, answered by the target cluster's own RBAC, held to their
namespace scope and written to the audit trail exactly like a `kubectl` typed
on a laptop. A shell holding a credential of its own would undo the whole
access model in one feature, which is why it does not have one.

!!! note "It is not a way around the tunnel"
    A `view` grant opens a shell that can read. An `edit` grant opens one that
    can write what `edit` can write. Nothing about being inside the cluster
    changes what the shell may do — the pod is powerless by construction, and
    all of its reach arrives as the operator's own credential.

## Who may open one

Anyone the cluster is granted to. Gating the surface behind administrator
would gate the *button* rather than the *reach*, which protects nothing and
costs the person with a read-only grant the one place they could have run
`kubectl describe`.

## How long it lives

Two clocks, because they fail in different directions:

| Clock | Default | Enforced by |
| --- | --- | --- |
| Idle | 1 hour without a keystroke | kubemg's reaper, every 2 minutes |
| Absolute | 8 hours | the pod's own `activeDeadlineSeconds` |

The absolute deadline is written **into the cluster**, so a bastion that is
down, upgrading or has lost the tunnel for a day is not what stands between a
forgotten shell and the end of it. The idle clock lives on the pod as an
annotation (`kubemg.io/shell-last-activity`), stamped at most once every few
minutes while somebody is typing — which is also what makes the reaper
stateless across replicas.

The lifetime is capped by the kubeconfig ceiling
([Kubeconfigs](../access/kubeconfigs.md)): a shell must never outlive the
credential inside it, or it becomes a terminal that looks alive and answers
nothing.

**Nothing written in a shell survives it.** The only writable paths are two
64 MiB `emptyDir` mounts (`/home/shell` and `/tmp`), and the root filesystem is
read-only. There is no persistent home directory, deliberately: a home that
survived would be an unaudited place to keep files on somebody's cluster.

## What bounds it

The pod is created with:

* `automountServiceAccountToken: false`, on a service account with no bindings
* `allowPrivilegeEscalation: false`, every capability dropped,
  `readOnlyRootFilesystem: true`, `runAsNonRoot` as uid 65532, and
  `seccompProfile: RuntimeDefault`
* no host network, no host PID namespace, no host path mount, and
  `enableServiceLinks: false`
* `restartPolicy: Never` — a crash loop must not silently produce a fresh
  terminal nobody opened
* 500m CPU and 256Mi memory as limits

## It is recorded like any other shell

Attaching to a browser shell is an `exec` through the proxy, so it is teed
into a session recording on exactly the same terms as the pod terminal, the
keystroke [guardrails](../access/guardrails.md) inspect what is typed into it
line by line, and it writes the usual two audit records at open and at close.
The recording notice sits above the terminal before the first keystroke. See
[Session recording](../audit/session-recording.md).

The audit trail names both identities: the row's **user** is the person, and
its **impersonated user** is `kubemg:shell-runner` for the pod's lifecycle
calls — creating it, seeding it, stamping its clock, deleting it. Those are
kubemg acting on its own behalf, and a trail that attributed a pod create to
an operator holding a read-only grant would be a lie.

The credential written into the shell is registered like any other issued
kubeconfig and appears under **You → My access**, marked as a shell. Revoking
it stops the shell's `kubectl` on its next call — the terminal is still there,
and it can no longer reach the cluster.

## Requirements

* The cluster must be registered in **agent mode**. A directly-connected
  cluster has no proxy route for the pod to dial back through, and the shell is
  refused there with that reason named.
* The cluster must be able to reach `KUBEMG_PUBLIC_URL` from a pod — the same
  egress the agent already uses.
* The agent's manifests must be current: the shell needs the `kubemg-shell`
  service account and the `kubemg-shell-runner` Role that ship with them.
  **An existing install picks these up only by re-applying its manifests.**

## Operator settings

Under **Admin → Agent settings**:

| Setting | Environment | Default |
| --- | --- | --- |
| Offer a browser shell | `KUBEMG_SHELL_ENABLED` | on |
| Shell image | `KUBEMG_SHELL_IMAGE` | `ghcr.io/kubemg/kubemg-shell:<version>` |
| Idle timeout | — | 60 minutes |
| Maximum lifetime | — | 8 hours |

Turning the shell off refuses **new** shells and leaves running ones alone —
a session somebody is mid-command in is not a setting. A server started
without a shell image cannot be talked into offering one from the database.

For an air-gapped site, mirror the shell image alongside the agent's and point
`KUBEMG_SHELL_IMAGE` at your copy; the image can also be rebuilt from
`shell/Dockerfile` against mirrored artefacts.
