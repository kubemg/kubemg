# The browser shell

The pod terminal answers what is wrong with **one pod**. The browser shell
answers everything else: `kubectl get` across a namespace, a `kubectl diff`
against a manifest, a `describe` of something no form covers. It is a terminal
in the console with `kubectl` on the path, one per person per cluster.

It exists so that the moment a question outgrows a form, the answer still
happens **inside** kubemg rather than in a terminal nobody can see.

## Opening one

The **Shell** button in the header, on any page of a cluster whose agent is
attached. It opens a dock along the bottom of the console and starts a session
straight away — there is no page to navigate to and no second button to press.

The dock is a layer *over* the console, not a page, for a specific reason: a
terminal is reached for in the middle of a question, and a page would take the
thing being asked about off the screen to answer it. It also keeps running
while you navigate — open a shell, go read the failing workload's events, come
back to the same prompt.

Closing the dock (`×`) hides it and leaves the session running; it is reclaimed
by its own idle window. Ending it (the power icon) deletes the pod now and
withdraws the credential that was inside it.

## What it actually is

A pod, on the target cluster, that kubemg creates when somebody asks for one:

* it runs kubemg's own image — busybox and `kubectl`, on a distroless base,
  **with no package manager in it**;
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

## What it does not have: `helm`

The image shipped with `helm` on the path for one release and no longer does.
It was removed rather than kept behind an accepted finding.

The reason is what an upstream release binary carries: helm's published
binaries embed the Go standard library they were built against, and that runs
behind Go's own security releases for weeks at a time. At the last count that
was **nine CRITICAL/HIGH in the helm binary alone**, none of them reachable by
a version bump — the newest release of *every* helm line carried the same set,
because they were all built with the same toolchain. The three ways out were to
accept them in an ignore file, to build helm from source against a current
toolchain, or not to ship it. Accepting them parks known findings inside the
container an operator's shell runs in; building it ourselves means shipping a
helm nobody else has, which is a worse thing to hand somebody than a missing
tool.

**Most of what people reach for `helm` for is in the console already**: install
from a registered [chart repository](helm-repositories.md), upgrade —
including to a new chart version — edit values, read history, roll back and
uninstall, all through the impersonated tunnel with the same RBAC and the same
audit trail. See [Helm](helm.md).

What genuinely left with the binary is the part the console
[deliberately does not do](helm.md#honest-limits):

* **`oci://` charts.** kubemg's chart repositories are `http(s)` only.
* **`helm get manifest`, `helm template`, `helm diff`.** A release's rendered
  manifest is decoded server-side and never returned to a client, because
  charts put generated passwords in it.
* **`helm test`**, and hook waiting — `--wait`/`--atomic` have no equivalent.
* **`helm lint` / `helm show`** against a chart whose repository nobody has
  registered.

If your work needs those, run helm from a workstation against a
[generated kubeconfig](../access/kubeconfigs.md) — which is the same proxied,
impersonated, audited path the shell used, just not from inside the cluster.

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
