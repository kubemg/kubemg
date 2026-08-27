# Exploring resources

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

- **Overview** — the object's own summary fields.
- **Describe & Events** — metadata, `status.conditions`, a bounded flatten of
  `spec`/`status`, and the cluster's own events against the object, newest
  first (unlike `kubectl describe`, which prints oldest first) — because a
  drawer is asked what just happened. A refused events read shows the
  cluster's own RBAC reason rather than failing the whole describe.
- **YAML** — the live manifest, editable for anything the write path allows.
- **Logs & Terminal** (pods) / **Logs** (workloads that support pooled logs)
  — see [Terminals and logs](terminals-and-logs.md).

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
