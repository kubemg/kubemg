# Events timeline

`GET /api/v1/clusters/:id/resources/events` answers "what broke in the last
fifteen minutes" across a whole cluster. It is the same read `describe` already
does for one object, with the object selector taken off: kubemg has been able
to show the events against a single Deployment or Pod since the Describe tab
shipped, but only once you already suspected that object. The timeline is the
question that comes first — the console page at **`/clusters/:id/events`**
opens on it directly, before anything has been suspected.

## What the page shows

Events are grouped **by involved object**, not printed one row per event. A
failing Deployment produces a `ScalingReplicaSet` from the deployment
controller, a `FailedCreate` from its ReplicaSet, and a `BackOff`/`Failed` per
pod — as flat rows that is forty lines describing one problem. As a group it is
one row (one `key`, `namespace/kind/name`), carrying:

- the **worst type** across every event folded into it (a group with one
  `Warning` among ten `Normal`s reads as a warning)
- the newest reason and message, since that is what the collapsed row shows
- `count` (every firing folded together) and `warnings` (how many of those
  were warnings) — both are totals, not object counts
- up to `maxGroupEntries` (20) distinct reason/type entries, each carrying its
  own first/last-seen and newest message, with `entries_truncated` set past
  that

Groups are ordered **newest first**, by the group's own last-seen time — the
same ordering the describe drawer already uses, because the question is "what
just happened," not "what happened when the object was created." A
**Warning-only filter** (`type=Warning`) narrows what feeds the grouping
rather than reordering it, so a warning from forty minutes ago never outranks
a failure from thirty seconds ago just because the filter was applied.

The page can also narrow to one object (`kind`/`name` query parameters), which
is exactly how a `ResourceInsights` alert or `ClusterWorkloadSummary`'s
attention list link in — see `eventsHref` in `lib/navigation.ts` — and to one
namespace, or every namespace the caller's grant covers.

## Both Event shapes, decoded

A core `v1` Event carries `firstTimestamp`/`lastTimestamp`/`count`. An event
written through the newer `events.k8s.io` API arrives on the **same** core
list, but with those fields empty and its time in `eventTime`/`series`
instead. `eventObject.view()` decodes both: it prefers `lastTimestamp`, falls
back to the series' `lastObservedTime`, then `eventTime`, then
`firstTimestamp`, and takes `count` from whichever of `Count` or
`Series.Count` is larger. Reading only the older shape would show a cluster's
newest events — the ones most likely written the new way — with no timestamp
at all.

## The bounded scan, and what "partial" means

A page of the event list is taken in **key order** (namespace/name), and an
Event's name is `<object>.<hex>` — so one page of a busy cluster is an
alphabetical slice by involved object, not the newest anything. Reading one
page and calling it "the newest" is wrong in a way that only shows up once a
cluster is big enough to exceed a page: production.

So the (non-buffered — see below) read follows the `continue` token under a
shared budget:

| Bound | Default | Purpose |
|---|---|---|
| `eventPageSize` | 500 | one page of the list read |
| `maxEventScan` (`KUBEMG_EVENT_SCAN_LIMIT`) | 4000 | total events read and folded before the answer is declared partial |
| `maxEventRequests` | 12 | round trips one page view may cost, whatever the scan budget would otherwise allow |
| `maxEventGroups` | 200 | groups kept after folding |
| `maxGroupEntries` | 20 | distinct reasons kept inside one group |

The scan budget is **global to the request**, not per namespace, because an
all-namespaces read fans out to one call per namespace — a per-page bound
would multiply by the fan-out instead of bounding it. When the walk stops
early, `truncated: true` is set and `scanned`/`available` report how much was
read against the API server's own `remainingItemCount` (only offered on an
unfiltered list). `total_groups` reports how many groups existed before the
200-group cap applied. The page states all of this rather than quietly
presenting a slice as the whole cluster.

An **empty page carrying a `continue` token is not the end of the list** — the
API server returns one whenever its scan skipped a page's worth after a
`fieldSelector` narrowed it — and `walkEventPages` is written to that rule
explicitly.

Events with their own RBAC can be refused independently of the object list: a
refusal narrows the answer (`events_available: false` with the cluster's
reason) rather than failing the whole request, and on an all-namespaces read
`unreadable_namespaces` names which namespaces refused when only some did.

## The buffered path: a watch instead of a scan

Whenever it can, the timeline answers from a **watch-fed ring buffer** instead
of a paginated list — one per cluster, started lazily by the first person who
opens a timeline on it. The ring:

- holds up to 5000 distinct events, deduplicated by UID
  (a repeating event is a `MODIFIED` on the same object, not a new one), and
  discards anything older than `eventBufferAge` (one hour — the same window
  Kubernetes itself keeps events for by default)
- is filled **cluster-wide** under a synthetic cluster-admin identity
  (`kubemg:event-watcher`) and filtered **per caller** on read, by the same
  namespace list a scoped grant would otherwise fan out over (`visibleTo`) —
  this is the one place a namespace filter is enforced by kubemg rather than
  refused by the API server
- stops itself after `eventWatchIdle` (15 minutes) of nobody reading it, so a
  fleet nobody opens a timeline on costs nothing
- re-lists (establishing a fresh `resourceVersion`) then watches, re-listing
  again whenever the watch ends — a watch ending is treated as normal
  (`eventWatchRetry`, 5s), only a failed *open* backs off further
  (`eventResyncBackoff`, 1 minute)

When the ring is warm (`synced`), the response carries `buffered: true` and
`buffered_at`, and "newest first" becomes a fact about the whole cluster
rather than a claim about a sample — because the ring holds everything the
cluster produced in the last hour, not a bounded page of it. The paginated
scan is the fallback: a cold ring (nothing synced yet), a server run without
the watcher, or a cluster whose watch is being refused all fall through to it
transparently, which is also what makes the very first page view — the one
that starts the watch — return something.

The watch lives in the **backend**, never the agent: the agent's ServiceAccount
holds no permission on any resource at all beyond `impersonate`, and giving it
a standing cluster-wide grant on events would be the first standing privilege
it has ever needed.

## Namespace scope and caching

The timeline follows the same scope rules as every other resource list: a
namespace-scoped grant reads its granted namespaces (fanned out, one call
each, under the shared scan budget), an unscoped grant reads cluster-wide.
Answers are held for `eventCacheTTL` (`KUBEMG_EVENT_CACHE_TTL`, default 30s) —
six times the plain resource cache's default, because no kubemg write ever
produces an Event (nothing here can go stale in a way a write should
invalidate), and because this is the one page a whole incident's worth of
people open at the same moment: without the longer hold, a dozen people
watching one outage turns into a dozen cluster-wide event `LIST`s against the
API server that is already having a bad day. `Cache-Control: no-cache` — what
the page's Refresh button sends — always bypasses it.

## Links from an object's alerts

`ResourceInsights`' pilot header and `ClusterWorkloadSummary`'s attention list
both name objects that are in trouble (a `CrashLoopBackOff`, a scaled-to-zero
workload, and so on); "why" is answered by what the cluster recorded against
that object, not by the object's own status. Both link into the timeline
through the same `eventsHref(clusterId, namespace, kind, name)` helper in
`lib/navigation.ts`, narrowed to that namespace and (when a name is present)
that object — one link, so the two surfaces can never format it differently.

## See also

- [Describe](managing.md) and the drawer's Describe & Events tab, for the same
  read scoped to one object.
- [Cluster actions](actions.md) for the workload writes an alert might lead to.
