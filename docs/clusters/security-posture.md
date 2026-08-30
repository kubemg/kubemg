# Security posture

`GET /api/v1/clusters/:id/resources/posture` and the console page at
`/clusters/:id/posture` evaluate seven fixed rules over fields the Explore
lists already fetch — a container's `securityContext`, a volume's
`hostPath`, a pod's host namespaces, a container's `resources.limits`, a
ServiceAccount's and a pod's `automountServiceAccountToken`, and whether a
namespace has any `NetworkPolicy` at all. Nothing here adds a permission or a
dependency; it widens the *decode* of objects Explore's own routes already
read, and evaluates a fixed rule set over the result.

!!! warning "This is not a vulnerability scanner"
    kubemg holds no image registry credential and no CVE feed, and nothing
    here inspects an image's contents — only manifest fields the API server
    already served. An image's known vulnerabilities belong to whatever
    already scans your registry; if the cluster has a registry console
    registered, a finding's image links there rather than kubemg guessing.
    This disclaimer (`non_goal_notice`) travels on every scan response and is
    rendered on the page, not left in a comment.

## The seven rules

Findings are ordered by what they **permit** (`permits`), highest first —
never by how many times a rule fired. Four of the seven are named [Kubernetes
Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
controls; the other three are explicitly marked as **not** covered by any PSS
profile, with a one-line reason, rather than left blank. `pss_unchecked` on
every response names every baseline and restricted control this scan does
**not** evaluate (HostProcess Containers, added Capabilities, Host Ports,
AppArmor, SELinux, `/proc` mount type, Seccomp, Sysctls, Volume Types,
Privilege Escalation, and more) — a clean scan is "clean on the four PSS
controls this checks," never "baseline- or restricted-compliant."

| Rule id | Title | Permits | Field | PSS control |
|---|---|---|---|---|
| `privileged_container` | Privileged container | 100 | `securityContext.privileged` | Baseline — Privileged Containers |
| `host_namespace` | Shares a host namespace | 90 | `hostNetwork` / `hostPID` / `hostIPC` | Baseline — Host Namespaces |
| `hostpath_volume` | hostPath volume | 80 | `volumes[].hostPath` | Baseline — HostPath Volumes |
| `namespace_no_network_policy` | No NetworkPolicy in this namespace | 55 | (namespace-level) | not a PSS control |
| `automount_default_service_account` | Default ServiceAccount token automounted | 45 | `automountServiceAccountToken` | not a PSS control |
| `no_nonroot_declaration` | No non-root user declared | 30 | `securityContext` | Restricted — Running as Non-root |
| `no_resource_limits` | No resource limits | 10 | `resources.limits` | not a PSS control |

**Privileged container** (100) — a container's `securityContext.privileged`
is `true`. It has every capability the node's own root has, including
reconfiguring the kernel and reaching every device on the host. Remediate by
removing `privileged: true` and granting only the specific `capabilities`
the container actually needs.

**Shares a host namespace** (90) — the pod sets `hostNetwork`, `hostPID`, or
`hostIPC`. It shares the node's network, process, or IPC space with every
other pod scheduled there. Remediate by removing the setting unless the
workload genuinely needs node-level access (a CNI or node-monitoring
DaemonSet, typically).

**hostPath volume** (80) — a volume mounts a path from the node's own
filesystem. Whatever a container with this mount writes is there for every
other pod later scheduled on the same node. Remediate with a `PersistentVolumeClaim`
or another cluster-managed volume type instead.

**No NetworkPolicy in this namespace** (55) — fires once per namespace, only
when the namespace actually has a workload in it and zero `NetworkPolicy`
objects exist. An empty namespace with no policies is not a finding; a
namespace with pods and no policy means every pod there is reachable from
wherever the cluster's network otherwise allows. Remediate by adding a
default-deny `NetworkPolicy` and opening only the traffic each workload
needs — see the coverage/reachability tools below for exactly which pods are
uncovered.

**Default ServiceAccount token automounted** (45) — the workload names no
ServiceAccount (or explicitly names `default`), and nothing — neither the
pod nor the ServiceAccount — sets `automountServiceAccountToken: false`
(Kubernetes' own documented default is to mount it). Any process in any
container can then present that identity to the API server, whatever it
turns out to be bound to, without the workload having asked for an identity
by name. Remediate by setting `automountServiceAccountToken: false`, or by
naming a purpose-built ServiceAccount if the workload does need the API.

**No non-root user declared** (30) — nothing in the pod's or a container's
`securityContext` sets `runAsNonRoot: true` or a non-zero `runAsUser`. This
is **not** a claim that the container runs as root — the image's own `USER`
directive decides that, and kubemg cannot see the image — only that nothing
in the manifest rules root out. Remediate by setting `runAsNonRoot: true`
(and a non-root `runAsUser` if the image needs one specified).

**No resource limits** (10) — a container declares **neither** a CPU nor a
memory limit (missing just one alone does not fire this rule; that would be
a narrower, noisier finding). It can consume as much of the node as is free,
at the expense of whatever else is scheduled there. Remediate by setting
`resources.limits.cpu` and/or `resources.limits.memory`.

Init and ephemeral containers are evaluated identically to main containers
for privilege, hostPath exposure and resource limits — a privileged init
container still touched the node while it ran — and are labelled with an
`init:`/`ephemeral:` prefix so a reader can tell which kind fired. A
Deployment/StatefulSet/DaemonSet's pod template is evaluated **once per
workload**, not once per running pod, so ten identical replicas produce one
finding rather than ten. A Pod with no owner (`ownerReferences` empty) is
evaluated directly; an owned pod is skipped, since its owning workload was
already evaluated through its template.

## Scan route and scope rules

`GET .../resources/posture` scopes exactly like every other resource list —
one namespace, or every namespace the caller's grant covers, fanned out one
read per namespace under `maxFanOut`. It reads (in order) ServiceAccounts and
NetworkPolicies once per namespace up front, then every Deployment /
StatefulSet / DaemonSet and every bare Pod, evaluating as it goes. A read the
caller's grant cannot make (say, ServiceAccounts but not NetworkPolicies)
narrows the scan's coverage rather than failing the whole request — it is
named in `unavailable` with the cluster's own reason, once per resource kind
regardless of how many namespaces refused it.

Two independent bounds keep a scan safe on a large cluster: `maxPostureScanObjects`
(4000) caps how many workload templates and bare pods are evaluated at all,
setting `truncated: true`; `maxPostureFindings` (1000) caps the findings
actually returned regardless of how many objects were scanned, setting
`findings_capped: true`. Narrowing to one namespace is how to get a complete
answer over a smaller scope.

The scan is **entirely read-only** and rides the same impersonated, audited
tunnel as every other list read — no new permission, no new cluster call
shape, no write to the target cluster at any point.

## Working through the list

The scan ranks every finding by what it **permits** and returns them in that
order; the console turns that ranking into something a security team can work
through rather than only read.

### Severity is the ranking, banded

`permits` is the server's number and the console derives four bands from it
rather than inventing a second scale — a browser-side severity would be free to
disagree with the order the response arrives in.

| Band | `permits` | Rules |
| --- | --- | --- |
| Critical | ≥ 90 | Privileged container; shares a host namespace. Owns the node, or everything else on it. |
| High | ≥ 80 | hostPath volume. Arbitrary node filesystem, bounded by the mount rather than by policy. |
| Medium | ≥ 45 | No NetworkPolicy; automounted default ServiceAccount token. Both widen what a compromise reaches; neither is one. |
| Low | below | No non-root declaration (genuinely uncertain — the image may already run non-root, the manifest just does not say so); no resource limits (a noisy neighbour, not an escape). |

The distribution sits above the list and carries two numbers per band: the total
and how many are still unacknowledged. They answer different questions — the
total is the shape of the cluster, the open count is the work — and a fully
triaged cluster showing only totals would look permanently alarming, which is how
a page like this stops being read. Each band is also the filter: pressing one
narrows the list to it, pressing it again clears it.

Every row carries a severity stripe. An acknowledged row keeps its band and loses
its saturation: the finding is no less severe for having been accepted, and
drawing it as though it were would hide what somebody signed off on.

### Grouping, filtering and export

Grouping by **severity**, **namespace** or **rule**; `Ranked` keeps the server's
own order, so "no grouping" is a real choice rather than the absence of one.
Within a group the ranking is preserved rather than re-sorted — a group ordered
alphabetically would bury its worst row. Namespace and rule groups lead with
whichever carries the worst finding.

The search box matches the object's name, its namespace, the rule title and the
field the finding names. It deliberately does **not** search the message: a
substring search over prose matches almost everything and makes the box feel
broken.

Acknowledged findings are hidden by default and shown with a checkbox. That is a
filter and never a deletion — an acknowledgement is a decision somebody recorded
with a reason, and it stays reviewable.

**Export** writes the rows currently on screen, filtering included, as CSV.

Unlike the [audit trail's export](../audit/trail.md), which is a server route,
this one is built in the browser. The trail is a table this server owns and
re-reading it is free; a posture scan is a live read of a cluster across every
granted namespace through the tunnel, so a server-side export would scan the
cluster a second time to produce a file. That costs the customer's API server
twice for one answer, and the second scan could disagree with the first, because
the cluster moves — an export that does not match the screen it came off is not
evidence.

### The disclaimers are folded, not cut

Both notices — the non-goal statement and the Pod Security Standards coverage
gap, with its full list of unchecked controls — used to open the page ahead of a
single finding, so a security team was shown the limits of the scan before the
scan. Every word is still there, in a "What this checks, and what it does not"
disclosure that starts closed. None of it could be cut: all three statements are
claims about what is *not* being asserted, and dropping any of them would leave
the page quietly overstating itself.

## Acknowledging a finding

A workload can trip a rule on purpose — a debug pod running privileged to
drive a hardware test rig, a DaemonSet that has to be `hostNetwork` to do its
job — and needs a way to say so without the posture list turning into noise
nobody reads twice.

- `POST /api/v1/clusters/:id/resources/posture/ack` — body `{kind, namespace,
  name, rule, reason}`. `reason` is **required**: an acknowledgement with no
  reason would be a mute button, not an auditable decision.
- `DELETE /api/v1/clusters/:id/resources/posture/ack?kind=&namespace=&name=&rule=`
  — removes an acknowledgement, putting the finding back into the plain,
  unreviewed list on the next scan.

**Acknowledging never removes the finding.** It stays in the list on every
future scan, ranked exactly where it would sort otherwise, now carrying who
accepted it (`ack_by`), when (`ack_at`), and their stated reason
(`ack_reason`) — visible as an acknowledged finding, not a deletion.

**Who may acknowledge**: reading the whole scan needs only the grant every
other resource list needs — a `view` grant answers "what is wrong here" as
fully as `edit` does. Silencing a finding is different: `requirePostureWriteGrant`
refuses a plain `view` grant, because a control anyone with read access can
switch off is not a control. The same bar every actual cluster write (scale,
restart, the manifest editor's PUT) already clears, even though an
acknowledgement never touches the tunnel — it is purely a row in kubemg's own
database.

**How long it lasts**: indefinitely, until explicitly removed with the
`DELETE` above. There is no expiry or TTL on an acknowledgement — it is a
recorded decision, not a snooze.

Both the acknowledge and unacknowledge writes are recorded in the audit trail
under the `security-posture/{kind}/{name}/{rule}` resource, with the reason
carried in the audit row's free-text error field — the one precedent this
non-proxy write follows, on the same basis as JIT decisions and terminal
recording access.

## NetworkPolicy coverage and reachability

Two related, read-only routes derive what NetworkPolicy objects actually
declare — not what a CNI enforces and not a live connectivity test — and are
worth knowing about from this page even though they surface per-object rather
than in the posture scan:

- `GET .../resources/networkpolicies/coverage?namespace=` — namespace-level
  summary: how many pods are covered for ingress and for egress separately
  (a policy can cover one direction and leave the other open), with a
  bounded sample of uncovered pod names.
- `GET .../resources/networkpolicies/reachability?kind=&name=&namespace=` —
  one workload's own view: which policies select it, what may reach it, what
  it may reach, and whether **nothing** selects it in a namespace where other
  things are governed (the case that turns "not covered" from a shrug into an
  actual finding). Supported kinds are `pods`, `deployments`, `statefulsets`,
  `daemonsets`, `jobs` — CronJobs are absent because they own Jobs, not pods,
  and have no pod template of their own.

Both carry a disclaimer on every response: selectors are evaluated for real
(`matchLabels` and every `matchExpressions` operator), but ports and
protocols inside a rule are **not** evaluated, so a peer named as able to
reach a workload can do so only if the rule's ports allow it too. Both are
single-namespace reads (a NetworkPolicy's reach never crosses a namespace
boundary) that ride the same impersonated tunnel and degrade to
`available: false` with a reason when the underlying list is refused, rather
than failing outright.

## See also

- [Cluster RBAC visibility](../access/rbac-visibility.md) for what a
  workload's ServiceAccount is actually bound to.
- [Guardrails](../access/guardrails.md) for policy that blocks a write before
  it happens, as opposed to reporting on what already exists.
