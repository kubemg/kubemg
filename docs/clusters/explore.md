# Browsing resources

Explore is kubemg's resource browser: live cluster state, read the way `kubectl` would read it. Use it to list, inspect, edit and act on objects. Which cluster you are reading is in the address (`/clusters/:id/<resource-key>`), so a click in the fleet list navigates and the heading, highlight and reads cannot disagree.

## The sidebar inventory

The tree is a **fixed** inventory (namespaces, workloads, pods, services, ingresses, storage, config, quotas and limits, RBAC, nodes) plus the custom resources the cluster actually serves. Discovered sections always sit **below** the fixed inventory.

- **HTTPRoutes and VirtualServices** appear only when the cluster serves them, and get a proper hostname/gateway/rules table. Every other CRD gets a generic name/kind/age table.
- **Other CRDs are grouped by API group family.** `kafka.strimzi.io` and `core.strimzi.io` both become *strimzi.io*. A family with **two or more** kinds gets its own section; a single-kind family lands in **Other**, at the bottom.
- Discovered sections and **Other** **start collapsed**. A section holding your current selection opens anyway.
- An administrator can hide CRDs from the sidebar: see [Managing a cluster](managing.md#curating-which-crds-the-explore-sidebar-offers). Hiding is navigation only, never access control.

While discovery is still running the sidebar waits. If it finds no CRDs, it falls back to Pods.

<figure markdown>
  ![The cluster tree](../assets/screenshots/explore-sidebar.png)
  <figcaption>The cluster tree. The fixed inventory comes first, then the sections discovered from the CRDs this cluster actually serves, which start collapsed.</figcaption>
</figure>

### Quotas & Limits

`ResourceQuota`, `LimitRange` and `PodDisruptionBudget` have a collapsed section of their own. Open it when something will not schedule, a drain hangs or a rollout stalls and no other list says why. A PDB's **Allowed** column (zero disruptions permitted) is drawn as a state, not a figure.

Quantities are shown **as the cluster wrote them** (`500m`, `2Gi`, `50%`). A quota's `used` is blank, not `0`, until the quota controller has counted. An undeclared LimitRange bound is blank, not `0`.

### HorizontalPodAutoscalers and ReplicaSets

Both sit under **Workloads**.

- **HPAs** are read as `autoscaling/v2` only. A cluster serving only `v1` is told the kind is not served, rather than shown an empty list. A metric with no reading yet shows `—/80%`, not `0%/80%`. See [the notice the scale control carries](actions.md#an-autoscaler-owns-the-replica-count).
- **ReplicaSets** have **Owner** and **Revision** columns, so you can tell the two ReplicaSets of a Deployment mid-rollout apart. Zero desired replicas is the resting state of a superseded one and is drawn as idle, not as a fault.

## Favorites

A star on a resource row pins it to a **Favorites** group at the top of the tree. It is a shortcut, not a second view. Favorites are kept in your browser, not on the server, and are one set across every cluster; a kind the current cluster does not serve is simply absent.

## Namespace selection

Pick one namespace or **All namespaces**. Your choice is remembered and restored where it is still valid for the open cluster and your grant.

- With a namespace-scoped grant, **All namespaces** is answered from the grant: one read per granted namespace, merged, capped at 25 namespaces. The cluster is never listed on your behalf.
- Cluster-scoped kinds (nodes, PVs, storage classes, CRDs) are always read cluster-wide, and refused outright for a namespace-scoped grant.

## A namespace's own page

`/clusters/:id/namespaces/:name` shows one namespace as a page. Open it from the namespaces list or from the scope picker. It reuses surfaces that exist elsewhere, so its numbers match the lists one click away:

- workload and pod cards, as on the developer dashboard
- **Quotas & limits** tables
- **NetworkPolicy coverage**
- **Recent events** for the namespace, with a way through to the full timeline
- a **Traffic** panel (see [A namespace's map](#a-namespaces-map))

Switching clusters from here lands on the target's namespace list, since `payments` on another cluster is a different namespace.

## The pilot header

Pod and workload lists open on a header built from rows already loaded, so it costs no extra read.

- **Pods** are bucketed by phase *and* readiness. A `Running` pod failing its readiness probe is not counted healthy.
- **Workloads** are bucketed by ready against desired. Desired `0` is *scaled to zero*, not an outage.
- Alerts use the cluster's own container-state word (`CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`).
- Empty buckets are not drawn. Clicking a reading **filters the list** to those rows.
- Services draw no header: they have no health of their own.

### The namespace block

With **one namespace** selected, every list under *Workloads* adds a row under the bar. It is not drawn under *All namespaces*.

| Cell | What it shows | Source |
|---|---|---|
| **Consumption by workload** | Donut of live CPU or memory, one slice per workload. Seven are named, the rest fold into *N more*. | metrics-server. Without it: *No live sample*, with the reason. |
| **Restarts** | Total restarts, how many pods, the worst three. On the pod list, clicking narrows the table. | Pod list |
| **Image pull** | Pods in `ImagePullBackOff`, `ErrImagePull` or `InvalidImageName`. | Pod list |
| **Throttled** | Pods throttled in more than **25%** of CPU periods over the header's time range. | The cluster's metrics datasource. Without one, the cell says so. |
| **At limit** | Pods whose last run was `OOMKilled`, or using **90%** or more of a container's own CPU or memory limit. | Pod list plus metrics-server. A namespace with no limits says so. |

**Show usage history** opens per-pod CPU and memory curves from the metrics datasource. It starts closed and the console remembers your choice. Every pod a cell names opens in the usual drawer.

## Filters and paging

The name filter narrows the loaded page in your browser. Lists are read in pages because the agent cannot carry a response over 8 MB through the tunnel.

- **250 items per page.**
- **2000 items per read**, across all pages and namespaces.

Truncation is never silent: when a read hits a bound, the console says the list is incomplete.

??? info "Why it works this way"
    An empty page that still carries a continue token is not the end of a list, and a continue token the API server has compacted (`410 Gone`) is treated as truncation, not failure. Pods are 8-15 KB apiece, so paging alone would still pull tens of megabytes through a tunnel built for kilobytes.

## Counts

The number beside a collapsed section is read cheaply from the API server (one item requested, the total taken from its own remaining-count), so cost does not grow with cluster size. Counts are fetched only for sections you can see, never on a timer, and never for what the name filter reveals. Helm releases have no count.

## The detail drawer

One drawer, one object, four tabs.

- **Overview** shows the object's summary fields.
    - A **ConfigMap** shows each key with its value (cut at 64 KiB; binary data as a size).
    - A **Secret** shows keys and sizes, never a value. With the reveal-secrets capability each key has a **Reveal** button; the read is recorded in the audit trail first. Without it, the drawer says who grants it.
    - Ingress, HTTPRoute, VirtualService and Service open with the [traffic map](#the-traffic-map).
    - Workloads, pods, Jobs, CronJobs and volume claims on a cluster with an Alertmanager have an **Alerts** panel under **Dependencies**, with **Silence** and the alarms kubemg wrote. **Create alarm** in the toolbar opens the form there. See [Cluster alerts](../observability/alerts.md).
- **Describe & Events** shows metadata, conditions, a flattened spec/status and the object's events, **newest first**. A refused events read shows the cluster's RBAC reason instead of failing the tab.
- **YAML** is the live manifest, editable where the write path allows.
- **Logs & Terminal** (pods) or **Logs** (workloads): see [Terminals and logs](terminals-and-logs.md).

### The traffic map

At the top of an Ingress, HTTPRoute or VirtualService Overview, the map follows the route hop by hop, left to right:

| Column | What is drawn |
| --- | --- |
| Entry | Ingress hosts (with the TLS Secret each names), or the Gateways a route attaches to (`mesh` for a VirtualService with none). |
| Route | The object itself. |
| Service | Each Service a rule sends to, with type, ports and ready endpoints. |
| Workload | The owning Deployment, StatefulSet, DaemonSet or Job. |
| Pods | The pods the Service selects, unready first. More than four per workload fold into "+N more". |

Edges carry the rule (host, path, split weight, port). Point at a box to light its whole path; click an object to open it in the same drawer.

**Drawn as broken**, in red with the reason:

- the Service a rule names does not exist
- the port is not one the Service exposes (drawn on that rule's edge)
- the Service's selector matches no pods
- no ready endpoint, or fewer than all (degraded)
- a pod is failing, in its container's own word
- an HTTPRoute the gateway refused or could not resolve (`Accepted` / `ResolvedRefs` false; a cross-namespace backend without a ReferenceGrant shows here)
- an Ingress with no address yet (degraded)

On a **Service** the map reads the other way: routes **in the Service's namespace** that send to it, then its pods. Other namespaces are not searched, and the tab says so.

Every hop is read as you and audited. A hop the cluster refuses is drawn **refused** (dashed) with the cluster's reason. A hop into a namespace outside your grant is drawn **outside your access** and not read.

Limits:

- It never opens a Secret; a TLS Secret is only named.
- DestinationRules are not read.
- Non-Service destinations (external hosts, ServiceEntries) are drawn but not followed.
- At most ten Services are followed; the rest show "not followed".
- It is not live: **Refresh** reads again.

**Only what needs a look** (offered when a map has problems) narrows the drawing to the broken hops and every path through them.

### A namespace's map

A namespace's page draws a **Traffic** panel following every Ingress, HTTPRoute and VirtualService at once (up to 25 Services). Services no route reaches are not drawn.

### What a workload depends on

A workload or pod Overview has a **Dependencies** map under its pods, showing every object its pod template names and how it is used.

| Object | What is checked |
| --- | --- |
| ConfigMap | Read for **key names** only. Missing is broken; a missing named key breaks that edge. If every reference is `optional`, a warning. |
| Secret | **Never read.** Turns broken only when a pod reports it (`secret "db" not found`). An unmounted Secret volume cannot be told from an existing one, and the map says so. |
| ServiceAccount | Read. Missing means the controller cannot create pods. |
| PersistentVolumeClaim | `Pending` is a warning, `Lost` is broken; a bound claim is followed to its PersistentVolume. StatefulSet claims are named per replica (first four). |
| PersistentVolume | Cluster-scoped: a namespace-scoped grant sees it as outside your access, unread. |

A Helm release opens the drawer over its own panels (values, history); see [Helm releases](helm.md).

## Creating an object

**Create** in a list's header (offered even on an empty list) opens a sheet with a starter manifest. It posts to the collection the list is served from.

- The namespace is the list's own; a manifest naming another is refused, not redirected.
- It is refused under *All namespaces*.
- There is no diff step: the manifest on screen is the change.
- Some kinds (RBAC objects, Nodes) cannot be created this way, and the console says why.

## Deleting

Delete uses the same address, cluster RBAC, namespace scope, guardrails and audit trail as editing. The message reads *"marked for deletion"*, not *"deleted"*, because a grace period or finalizer can keep the object listed for a while.

A **Select** chip turns on a checkbox column for pods, workloads, jobs and cronjobs. A selection of eight is eight sequential calls, each audited and reported per row. See [Workload actions](actions.md#acting-over-a-selection).

## Caching and live reads

Reads are cached for a few seconds per person (`KUBEMG_RESOURCE_CACHE_TTL`) and never shared across identities. Any write clears the cluster's cache.

Lists, the drawer's Overview and Describe, the events timeline, node capacity and the cluster dashboard **re-read every 15 seconds**, but only while the tab is visible. A failed refresh keeps what is on screen and marks it *stale*. **Refresh** always asks the cluster directly.

One remembered switch pauses all live reads.
