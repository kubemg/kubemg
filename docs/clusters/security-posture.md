# Security posture

The posture page (`/clusters/:id/posture`) scans a cluster for seven risky manifest settings, ranks the findings by what they permit, and lets you acknowledge the ones that are intentional. Read it to see what is wrong, then work through the list.

The rules cover a container's `securityContext`, `hostPath` volumes, host namespaces, resource limits, ServiceAccount token automounting, and namespaces without any `NetworkPolicy`. It adds no permission or dependency; it only evaluates fields Explore already reads.

!!! warning "This is not a vulnerability scanner"
    kubemg holds no registry credential and no CVE feed and does not inspect images. It reads only manifest fields the API server served. Image vulnerabilities belong to whatever scans your registry; if the cluster has a registry console registered, a finding's image links there. This notice (`non_goal_notice`) is on every scan response and on the page.

## The seven rules

Findings are ordered by what they **permit**, highest first, never by how often a rule fired. Four rules are named [Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/) controls. The other three are marked as not covered by any PSS profile.

| Rule id | Title | Permits | Field | PSS control |
|---|---|---|---|---|
| `privileged_container` | Privileged container | 100 | `securityContext.privileged` | Baseline — Privileged Containers |
| `host_namespace` | Shares a host namespace | 90 | `hostNetwork` / `hostPID` / `hostIPC` | Baseline — Host Namespaces |
| `hostpath_volume` | hostPath volume | 80 | `volumes[].hostPath` | Baseline — HostPath Volumes |
| `namespace_no_network_policy` | No NetworkPolicy in this namespace | 55 | (namespace-level) | not a PSS control |
| `automount_default_service_account` | Default ServiceAccount token automounted | 45 | `automountServiceAccountToken` | not a PSS control |
| `no_nonroot_declaration` | No non-root user declared | 30 | `securityContext` | Restricted — Running as Non-root |
| `no_resource_limits` | No resource limits | 10 | `resources.limits` | not a PSS control |

A clean scan means "clean on the four PSS controls checked", never "baseline- or restricted-compliant". `pss_unchecked` on every response lists the controls not evaluated.

| Rule | Fires when | Fix |
|---|---|---|
| Privileged container | `privileged: true` | Remove it; grant only the specific `capabilities` needed |
| Shares a host namespace | Pod sets `hostNetwork`, `hostPID` or `hostIPC` | Remove unless it truly needs node access (a CNI or node-monitoring DaemonSet) |
| hostPath volume | A volume mounts a node path | Use a `PersistentVolumeClaim` or other managed volume |
| No NetworkPolicy | A namespace has a workload and zero `NetworkPolicy` objects (once per namespace; an empty namespace is not a finding) | Add a default-deny policy, open only needed traffic |
| Default ServiceAccount token automounted | Workload names no ServiceAccount (or `default`) and neither pod nor ServiceAccount sets `automountServiceAccountToken: false` | Set it to `false`, or name a purpose-built ServiceAccount |
| No non-root user declared | No `runAsNonRoot: true` or non-zero `runAsUser` anywhere in the pod or container `securityContext` | Set `runAsNonRoot: true` (and a non-root `runAsUser` if needed) |
| No resource limits | A container declares **neither** CPU nor memory limit (one alone does not fire) | Set `resources.limits.cpu` and/or `.memory` |

"No non-root" does not claim the container runs as root; the image's `USER` decides that and kubemg cannot see the image.

Init and ephemeral containers are checked like main containers for privilege, hostPath and limits, labelled `init:` or `ephemeral:`. A Deployment, StatefulSet or DaemonSet template is evaluated **once per workload**, not per replica. A bare Pod is evaluated directly; an owned pod is skipped.

## Scan route and scope rules

`GET .../resources/posture` scopes like every other resource list: one namespace, or every granted namespace fanned out per namespace. It is read-only and uses the same impersonated, audited tunnel.

- A read your grant cannot make (for example ServiceAccounts but not NetworkPolicies) narrows coverage instead of failing. It is named in `unavailable` with the cluster's reason, once per resource kind.
- At most 4000 workload templates and bare pods are evaluated (`truncated: true` past that).
- At most 1000 findings are returned (`findings_capped: true`).
- Narrow to one namespace for a complete answer over a smaller scope.

## Working through the list

### Severity is the ranking, banded

The console derives four bands from the server's `permits` number.

| Band | `permits` | Rules |
| --- | --- | --- |
| Critical | ≥ 90 | Privileged container; shares a host namespace |
| High | ≥ 80 | hostPath volume |
| Medium | ≥ 45 | No NetworkPolicy; automounted default ServiceAccount token |
| Low | below | No non-root declaration; no resource limits |

The distribution above the list shows, per band, the total and how many are still unacknowledged. Pressing a band filters to it; pressing it again clears. An acknowledged row keeps its band but is drawn less saturated.

### Grouping, filtering and export

- **Group** by severity, namespace or rule. `Ranked` keeps the server's order. Ranking is preserved within a group.
- **Search** matches the object name, namespace, rule title and field. It does not search the message.
- Acknowledged findings are hidden by default; a checkbox shows them. This is a filter, never a deletion.
- **Export** writes the rows on screen, filters included, as CSV. It is built in the browser, unlike the [audit trail's export](../audit/trail.md), so it does not scan the cluster a second time and always matches the screen.

### The disclaimers are folded, not cut

The scan's limits (the non-goal statement and the list of unchecked Pod Security Standards controls) sit in a **What this checks, and what it does not** disclosure that starts closed.

## Acknowledging a finding

A workload may trip a rule on purpose, such as a debug pod running privileged. Acknowledge it so the list stays readable.

- `POST /api/v1/clusters/:id/resources/posture/ack` with `{kind, namespace, name, rule, reason}`. `reason` is **required**.
- `DELETE /api/v1/clusters/:id/resources/posture/ack?kind=&namespace=&name=&rule=` removes it.

**Acknowledging never removes the finding.** It stays in every future scan, ranked where it would sort, with who accepted it (`ack_by`), when (`ack_at`) and the reason (`ack_reason`). It lasts until you remove it; there is no expiry.

**Who may acknowledge:** reading the scan needs only a `view` grant. Acknowledging needs more than `view`, the same bar as other cluster writes, even though it only writes a row in kubemg's own database.

Both writes are audited under `security-posture/{kind}/{name}/{rule}`, with the reason in the row's free-text error field.

## NetworkPolicy coverage and reachability

Two read-only routes show what NetworkPolicy objects **declare**, not what a CNI enforces and not a live connectivity test.

- `GET .../resources/networkpolicies/coverage?namespace=` gives a namespace summary: pods covered for ingress and egress separately, with a bounded sample of uncovered pod names.
- `GET .../resources/networkpolicies/reachability?kind=&name=&namespace=` shows one workload's policies, what may reach it, what it may reach, and whether nothing selects it in a namespace where other things are governed. Kinds: `pods`, `deployments`, `statefulsets`, `daemonsets`, `jobs` (not CronJobs).

Selectors are fully evaluated, but **ports and protocols in a rule are not**: a listed peer can reach the workload only if the rule's ports allow it too. Both are single-namespace reads, and return `available: false` with a reason if the list is refused.

## See also

- [Cluster RBAC visibility](../access/rbac-visibility.md) for what a workload's ServiceAccount is bound to.
- [Guardrails](../access/guardrails.md) for policy that blocks a write before it happens.
