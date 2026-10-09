# Events timeline

The events page (`/clusters/:id/events`) answers "what broke in the last fifteen minutes" across a whole cluster. Open it first, before you know which object to suspect.

## What the page shows

Events are grouped **by involved object** instead of one row per event. One failing Deployment would otherwise produce forty lines. Each group row carries:

- the **worst type** among its events (one `Warning` among ten `Normal`s reads as a warning)
- the newest reason and message
- `count` (all firings folded together) and `warnings` (how many were warnings)
- up to 20 distinct reason/type entries, each with its own first/last seen and newest message (`entries_truncated` is set past that)

Groups are ordered **newest first** by their own last-seen time. The **Warning-only** filter narrows what is grouped; it does not reorder, so an old warning never outranks a fresh failure.

You can narrow to one namespace, or to one object (the `kind` and `name` parameters). Alerts on an object's header and the cluster dashboard's attention list link here already narrowed.

Namespace-scoped grants read their granted namespaces only. Unscoped grants read the whole cluster.

## What "partial" means

Without the live buffer (below), a read walks the event list page by page under a shared budget.

| Bound | Default | Purpose |
|---|---|---|
| Page size | 500 | One page of the list read |
| Scan limit (`KUBEMG_EVENT_SCAN_LIMIT`) | 4000 | Events read before the answer is declared partial |
| Requests | 12 | Round trips one page view may cost |
| Groups | 200 | Groups kept after folding |
| Entries per group | 20 | Distinct reasons kept in a group |

The scan budget covers the whole request, including all-namespaces fan-out. When the walk stops early the response has `truncated: true` with `scanned`/`available`, and `total_groups` says how many groups existed before the cap. The page says so rather than presenting a slice as the whole cluster.

If events are refused by the cluster's RBAC, the answer narrows (`events_available: false` with the cluster's reason) instead of failing. On an all-namespaces read, `unreadable_namespaces` names the refusals.

## The live buffer

Where it can, the timeline answers from a per-cluster buffer fed by a watch, started by the first person who opens a timeline on that cluster.

- Holds up to 5000 distinct events from the last hour.
- Is filled cluster-wide and filtered **per caller** on read, so a scoped grant sees only its namespaces.
- Stops after 15 minutes without readers, so an unwatched cluster costs nothing.
- When it is warm the response has `buffered: true` and `buffered_at`, and "newest first" is true of the whole last hour.
- A cold buffer, a server without the watcher, or a refused watch falls back to the scan above automatically.

??? info "Why it works this way"
    A page of the Events list is in key order (namespace/name), so on a large cluster one page is an alphabetical slice by involved object, not the newest events. That is why the scan follows continue tokens (an empty page with a token is not the end) and why the buffer exists.

    Events from the newer `events.k8s.io` API arrive on the same list with their time in a different field. The page reads both, so the newest events are never shown with no timestamp.

    The watch runs in the backend, not the agent. The agent holds no standing cluster-wide permission and should not gain one for events.

## Caching

Answers are held for 30 seconds by default (`KUBEMG_EVENT_CACHE_TTL`), longer than the normal resource cache. No kubemg write produces an Event, and a whole incident team opening one page at once should not become a dozen cluster-wide list calls. **Refresh** always bypasses the cache.

## See also

- [Managing a cluster](managing.md) and the drawer's Describe & Events tab, for the same read scoped to one object.
- [Cluster actions](actions.md) for the workload writes an alert might lead to.
