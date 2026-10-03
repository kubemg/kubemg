# Browsing resources

Explore is kubemg's resource browser: live cluster state, read the same way
`kubectl` would, with no privileged shortcut for the UI. Which cluster you are
reading is in the address (`/clusters/:id/<resource-key>`), not in page state
— the fleet list *is* the cluster switcher, so a click there navigates and
the highlight, the heading and the reads cannot disagree.

## The sidebar inventory

The tree (`ExploreSidebar`/`ClusterTree`) is built from a **fixed** inventory
— namespaces, workloads, pods, services, ingresses, storage, config, quotas
and limits, RBAC, nodes — plus whatever custom resources a particular cluster
actually serves.
Every discovered section sits **below** the whole fixed inventory: a mesh or
a Kafka operator is a layer over the Pods and Services everything else is
browsed through, so it must never push them down the column.

Discovered sections are derived from the cluster's own CRD list
(`discoverCategories`/`exploreCategories` in `lib/resources.ts`), read once
per cluster via `fetchCRDs`:

- **HTTPRoutes and VirtualServices** are *not* fixed entries — an entry that
  is always shown and usually answers "not installed" is a worse sidebar than
  one listing only what is there. They are keyed `plural.group` in
  `RICH_CRD_ITEMS`, which is what gives them their normalised
  hostname/gateway/rules table instead of the generic name/kind/age fallback
  every other CRD gets. That registry is the extension point: a CRD worth a
  real table gets an entry there.
- **Every other CRD is bucketed by its API group family** (`groupFamily`) —
  `kafka.strimzi.io` and `core.strimzi.io` both reduce to `strimzi.io`,
  because an operator usually serves several API groups under one domain and
  the domain root is what says "these are one area of work". A family with
  at least **2** kinds (`MIN_OPERATOR_KINDS`) earns its own section, id
  `operator:{family}`; a single-kind family lands in **Other**, at the very
  bottom, because one CRD is a row, not an area of work.
- `SHARED_GROUP_ROOTS` keeps registrar domains (`k8s.io`, `coreos.com`, …)
  from swallowing half the ecosystem under one name — under a shared root the
  identity is the label in front of it. `FAMILY_LABELS` fixes casing only
  (`istio.io` → Istio, `cert-manager.io` → cert-manager); a family with no
  entry there still gets a section, just named after its own domain string.
- Every `operator:` section and **Other** **start collapsed**, because they
  can run long and opening by default would push the fixed inventory off
  screen. A section holding the current selection opens anyway, and an
  explicit toggle always wins over that default.
- An administrator can **curate** which of a cluster's CRDs the sidebar
  offers at all — see [Managing a cluster](managing.md#curating-which-crds-the-explore-sidebar-offers).
  Curation removes a kind from navigation only; it is never access control.

`crds === null` (discovery still running) is deliberately distinct from `[]`
(discovery answered: none) — the sidebar waits on the first, and falls back
to Pods on the second.

### Quotas & Limits

`ResourceQuota`, `LimitRange` and `PodDisruptionBudget` get a section of
their own, and it **starts collapsed**. Nobody browses a ResourceQuota; you
come looking for one when something will not schedule and no other list shows
why — which is exactly the shape of a pod a quota rejected: it never became a
pod, so there is nothing running to look at and the event that said so has
scrolled away. A PDB is the other end of the same question: a drain that
hangs and a rollout that stalls are both a budget saying no while every
workload list looks healthy, which is why the **Allowed** column (zero
disruptions permitted) is drawn as a state rather than a figure.

Quantities in these lists are shown **as the cluster wrote them** (`500m`,
`2Gi`, `50%`). A quota's `used` is blank rather than `0` until the quota
controller has counted once, and a LimitRange bound that was not declared is
blank rather than `0` — `min: 0` and "no minimum" are different statements.

### HorizontalPodAutoscalers and ReplicaSets

Both sit under **Workloads**, where the thing they are about is. An HPA is
what decides a workload's replica count — see
[the notice the scale control carries](actions.md#an-autoscaler-owns-the-replica-count).
kubemg reads `autoscaling/v2` and nothing else; a cluster old enough to serve
only `v1` is told the kind is not served here rather than shown an empty list
that would read as "nothing is autoscaled". A metric with no reading yet
shows `—/80%`, not `0%/80%`, because those mean opposite things.

ReplicaSets carry an **Owner** and a **Revision** column, which is what makes
them worth listing separately: a namespace mid-rollout holds two ReplicaSets
for the same Deployment, and the revision is the only thing that says which
is which. Zero desired replicas is the resting state of every superseded
ReplicaSet and is drawn as idle, not as a fault.

<figure markdown>
  ![The cluster tree](../assets/screenshots/explore-sidebar.png)
  <figcaption>The cluster tree. The fixed inventory comes first, then the sections discovered from the CRDs this cluster actually serves, which start collapsed.</figcaption>
</figure>

## Favorites

A star on any resource row pins it to a **Favorites** group above everything
else at the top of the tree. It is navigation, not a second view: the pinned
row is the same link to the same list drawn a second time. The set lives in
the browser (`localStorage`, key `kubemg.favorites`) — like the deck choice —
because what one operator pins means nothing to anyone else, so there is no
round trip involved. It is one set across every cluster (`pods` means the
same thing everywhere), and a `crd:` key needs no special handling since the
Favorites group is built from *that* cluster's own inventory — a kind the
current cluster does not serve is simply absent from it. Rows keep the
inventory's own order rather than the order they were starred in.

## Namespace selection

A single-namespace picker, or **All namespaces** (`*` in the UI, sent to the
API as `all_namespaces=true`). The choice persists per user to
`kubemg_preferred_namespace` and is restored only where it is still valid for
the currently open cluster and grant. A namespace-scoped grant reading
"all namespaces" is answered **from the grant** — one read per granted
namespace, merged and sorted, capped at 25 namespaces (`maxFanOut`) — never
by listing the cluster, which would let a scoped caller enumerate namespaces
they were never given. A cluster-scoped kind (nodes, PVs, storage classes,
CRDs, …) is always read cluster-wide regardless of the namespace selection,
and is refused outright for a namespace-scoped grant on any list that would
otherwise reach past its scope.

## A namespace's own page

`/clusters/:id/namespaces/:name` is one namespace as a page rather than as a
row. It is reached from the namespaces list (the name is a link) and from the
scope picker while Explore is narrowed to one — both places somebody has
already said which namespace they mean.

It is a **composition of surfaces that already existed**, not a new read:

- the workload and pod cards are the developer dashboard's own
  (`ClusterWorkloadSummary`), given a namespace instead of every namespace, so
  they are the `lib/insights.ts` derivations and a count here cannot disagree
  with the same count in the list one click away;
- **Quotas & limits** are the Explore tables, rendered by the same
  `ResourceView` over the same rows — a namespace's quota is why a pod that
  never appeared never appeared, and it lived two clicks from the list that
  does not show it;
- **NetworkPolicy coverage** is the panel the reachability view already draws,
  disclaimer included;
- **Recent events** are the cluster timeline's own grouped rows, narrowed to
  this namespace and capped, with the way through to the full timeline.

Switching clusters from here lands on the target's **namespace list** rather
than on the same name: `payments` on another cluster is a different namespace,
or none at all.

## The pilot header

Pod and workload lists open on a header derived entirely from rows **already
loaded in the browser** — it costs no extra read and cannot disagree with the
table beneath it.

- **Pods** are bucketed by phase *and* readiness: `Running` alone is not
  treated as healthy, because a pod whose readiness probe is failing stays
  `Running` forever, which is exactly what a phase-only count would call
  fine.
- **Workloads** are bucketed by `ready` against `desired`, with `desired == 0`
  reported as *scaled to zero* rather than as an outage.
- Named alerts carry the cluster's own container-state word
  (`CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`) rather than a generic
  "not ready".
- Empty buckets are never drawn, and alerts are capped so the header stays a
  band rather than a second table.
- **Every reading is also a narrowing** — clicking *Failed* filters the list
  to those rows, using the same predicate the header counted with.
- A Service draws no header, deliberately: it has no health of its own, and
  deriving one from endpoints it does not own would be a claim the list
  cannot back up.

## Filters and paging

A name filter narrows client-side over the loaded page. Every list read
**pages** on the server, and that is a hard limit rather than tidiness: the
agent caps a response it carries back from the API server at 8 MB, and an
unpaginated all-namespaces pod list on a real cluster is refused as too large
to fit through the tunnel in one frame. Two bounds apply:

- **250 items per page** (`listPageSize`), bounding one frame.
- **2000 items per read** (`maxListItems`), bounding the whole read across
  every page *and* every namespace of a fan-out — pods, for example, are
  8–15 KB apiece with `managedFields`, so paging alone would still pull tens
  of megabytes through a tunnel sized for kilobytes.

**Truncation is never silent.** A response that hit either bound carries
`truncated`/`truncated_at`, and the UI states it rather than quietly showing
an incomplete list as if it were complete. An empty page that still carries a
continue token is *not* the end of a list — the API server returns one
whenever its scan skipped a page's worth — and a continuation token the API
server has since compacted answers `410 Gone`, which reads as truncation
(the pages already read stand) rather than as a failure.

## Counts

The number beside a collapsed section's row (`GET
.../resources/counts?keys=…`) is not produced by listing — counting by
listing is exactly what cannot work at this cost model. Instead each key is
read at `limit=1` and the count comes from the API server's own
`remainingItemCount`, so the cost of a count is flat in the size of the
cluster rather than proportional to it. Counts are batched (one round trip
for a whole column), bounded (48 keys / 96 calls per request), and read
**lazily and never on a tick** — a collapsed section asks for nothing, and
`ClusterTree` never counts what the *name filter* reveals, or every keystroke
would trigger a batch of cluster reads. Helm releases have no count, because
a release is a labelled Secret rather than a kind the API server counts.

## The detail drawer

One drawer, one object, four tabs — because finding out something is broken,
asking why, and changing it is one investigation rather than three:

- **Overview** — the object's own summary fields. A **ConfigMap** opens on its
  data: each key with its value (values over 64 KiB are cut, with the whole
  object on the YAML tab; binary data is shown as a size). A **Secret** opens on
  its keys and their sizes — never a value; if you hold the reveal-secrets
  capability, each key has a **Reveal** button that reads that one value and
  records it in the audit trail before it is shown, and otherwise the drawer
  says who grants it. On an Ingress, HTTPRoute,
  VirtualService or Service it opens with the **traffic map** — see
  [The traffic map](#the-traffic-map) below — because where a route sends its
  traffic is the first thing a route is opened for.
- **Describe & Events** — metadata, `status.conditions`, a bounded flatten of
  `spec`/`status`, and the cluster's own events against the object, newest
  first (unlike `kubectl describe`, which prints oldest first) — because a
  drawer is asked what just happened. A refused events read shows the
  cluster's own RBAC reason rather than failing the whole describe.
- **YAML** — the live manifest, editable for anything the write path allows.
- **Logs & Terminal** (pods) / **Logs** (workloads that support pooled logs)
  — see [Terminals and logs](terminals-and-logs.md).

### The traffic map

An Ingress, an HTTPRoute or a VirtualService says "send this host and path to
that Service", and everything that can go wrong with that sentence is invisible
from the route itself. The traffic map, at the top of the object's **Overview**,
follows it and draws every hop, left
to right:

| Column | What is drawn |
| --- | --- |
| Entry | The Ingress's hosts (with the TLS Secret each one names), or the Gateways and Istio gateways a route attaches to — or `mesh` for a VirtualService with none. |
| Route | The object itself: its class and address, or its hostnames. |
| Service | Every Service a rule sends to, with its type, ports and how many endpoints are ready. |
| Workload | The Deployment, StatefulSet, DaemonSet or Job that owns the pods — a ReplicaSet is followed to its Deployment. |
| Pods | The pods the Service selects, unready ones first. More than four per workload fold into one "+N more" box. |

Each edge carries the rule that sends traffic down it — host and path, the
weight of a split, the port. Point at any box to light its whole path (what
leads to it and what it leads to) and fade the rest; click one that is an
object to open it in the same drawer, so you can walk from a route to the pod
that is failing without closing anything. A pod opens on its own drawer with
logs and terminal.

**What it calls broken**, each drawn in red with the reason beside it and
listed in words under the drawing:

- the Service a rule names does not exist;
- the port a rule names is not one the Service exposes — drawn on that
  rule's edge, not on the Service, since another route may reach the same
  Service on a port it does expose;
- the Service's selector matches no pods;
- the Service has no ready endpoint (none of its pods passes its readiness
  probe), or fewer than all — drawn as degraded;
- a pod is failing — in its container's own word (`CrashLoopBackOff`,
  `ImagePullBackOff`), not a generic "not ready";
- for an HTTPRoute, a gateway controller has refused it or could not resolve a
  reference (`Accepted` / `ResolvedRefs` false on the route's status — a
  cross-namespace backend without a ReferenceGrant shows up here);
- an Ingress no controller has given an address to yet — drawn as degraded.

On a **Service** the map reads the other way: the Ingresses, HTTPRoutes and
VirtualServices in the Service's namespace that send to it, then the Service
forward to its pods. Routes in other namespaces are not searched, and the tab
says so.

Every hop is read as you, through the same tunnel as everything else on this
page, so it is in the audit trail and the cluster's RBAC decides each one:

- A hop the cluster refuses is drawn as **refused** (dashed) with the
  cluster's own reason; the rest of the map is still drawn.
- A hop into a namespace outside your grant — a VirtualService sending to
  `ledger.payments.svc.cluster.local` when you hold only `shop` — is drawn as
  **outside your access** and is **not read at all**.

What it deliberately does not do:

- **It never opens a Secret.** A TLS Secret is named on its host but not read,
  so looking at a route does not put a Secret read in the audit trail.
- **DestinationRules are not read.** An Istio subset is a label on the edge,
  not the set of pods it selects.
- Destinations that are not Services — an Ingress resource backend, a
  non-Service `backendRef`, a VirtualService host that is not
  `name` or `name.namespace.svc…` (a ServiceEntry or an external API) — are
  drawn but not followed.
- A map follows at most ten Services; more are drawn as "not followed".
- It is not live. **Refresh** (the circular arrow) reads the path again.

**Only what needs a look** (offered when a map has a problem and healthy hops
besides) narrows the drawing to the broken hops and every path through them —
the routes that reach a failing Service and the pods behind it, which is the
blast radius and the cause in one picture. On a busy map only the labels of
broken edges and of the path you point at are drawn.

### A namespace's map

A namespace's own page draws a **Traffic** panel with every Ingress, HTTPRoute
and VirtualService in the namespace followed at once (up to 25 Services). A
hop opens in the same drawer Explore uses. Services no route reaches are not
drawn there — open one for its own map.

### What a workload depends on

A Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob or Pod gets a
**Dependencies** map on its Overview, under its pods: every object its pod
template names, with how it is used on the edge (`env DB_PASSWORD`,
`volume config → /etc/app`, `image pull secret`, `service account`,
`env from every key`).

| Object | What is checked |
| --- | --- |
| ConfigMap | Read for its **key names** (never shown values): a missing ConfigMap is broken, a key the template names but the ConfigMap lacks breaks that edge. An absent ConfigMap every reference marks `optional` is a warning — the pods start without it. |
| Secret | **Never read.** Drawn as named; it turns broken when a pod reports it — a container waiting on `secret "db" not found` or `couldn't find key password in Secret …`. A Secret volume no pod has tried to mount yet cannot be told apart from one that exists, and the map says so. |
| ServiceAccount | Read; a missing one means the controller cannot create the pods. |
| PersistentVolumeClaim | Read: `Pending` is a warning, `Lost` is broken, a bound claim is followed to its **PersistentVolume** (capacity, reclaim policy, phase). A StatefulSet's `volumeClaimTemplates` are named per replica (`data-db-0`, …, the first four). |
| PersistentVolume | Cluster-scoped, so a namespace-scoped grant draws it as outside your access and does not read it. |

A Helm release opens the same drawer over its own two panels (values,
history) instead, since it has no manifest for the object route to address —
see [Helm releases](helm.md).

## Creating an object

`Create` in a list's own header (next to the name filter, offered even on an
empty list) opens `CreateResourceSheet` with a starter manifest for the
addressed kind. `POST .../resources/object` posts it to the collection path
the list itself is served from — the apiVersion must match what the addressed
kind actually serves, the namespace is the list's own address (a manifest
naming a different one is refused, never silently redirected), and
`all_namespaces` is refused outright for a create, since picking a namespace
on somebody's behalf is not kubemg's decision to make. There is no diff step
here: against an object that does not yet exist, the diff is the manifest
already on screen. A handful of kinds are not creatable this way (RBAC
objects, Nodes) — the console states why rather than silently omitting the
button.

## Deleting

`DELETE .../resources/object` is the *same address* as reading and writing
that object, so it reaches nothing the manifest editor could not already
reach: the cluster's RBAC, the namespace scope, the guardrails and the audit
trail all apply unchanged. It carries `propagationPolicy=Background`
(kubectl's own default), and the response reads *"marked for deletion"*
rather than *"deleted"* — a grace period or a finalizer can leave the object
in the list for a while after the call returns.

Selecting rows (the checkbox column, off until asked for via a **Select**
chip) offers bulk-shaped actions on pods, workloads, jobs and cronjobs — but
**there is no bulk API route**. A selection of eight is eight separate calls,
sequential, each its own audit record, reported per row — see
[Workload actions](actions.md#acting-over-a-selection) for why.

## Read caching and live reads

Every read here is a real tunnel round trip, an impersonated call and an
audit record — the right price for a question, the wrong one for the same
question asked three times in three seconds (a sidebar click back to a list
just left, a drawer opened over its own list). A short server-side cache (5s
default, `KUBEMG_RESOURCE_CACHE_TTL`) keyed on the caller's identity and
grant absorbs that; only a `200` is ever cached, and any non-GET invalidates
the whole cluster's cache scope so a scale or a restart shows up in the next
list rather than five seconds later.

Separately, Explore's lists, the drawer's Overview/Describe, the events
timeline, node capacity and the cluster dashboard **re-read themselves** every
15 seconds — but **only while somebody is actually looking**: nothing ticks
behind a hidden tab or an untouched one, coming back to the tab re-reads
immediately, and one remembered switch pauses the whole console's live reads.
A tick never draws a skeleton and never re-renders when the answer is
unchanged; a failed tick leaves what is already on screen and reports
*stale* rather than replacing a list somebody is reading with an error. This
is deliberately not a watch: a poll stops cleanly with the tab, where a
failed watch stream leaves a page that has quietly stopped updating with no
signal that it has.

**Refresh** is the one action that always asks the cluster — it bypasses both
caches (`Cache-Control: no-cache`), which the live tick deliberately never
sends, so several tabs of one console watching the same list collapse into
one tunnel round trip most of the time, and Refresh remains the thing that
guarantees a fresh answer on demand.
