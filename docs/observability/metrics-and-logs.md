# Metrics and logs

Once a [datasource](datasources.md) is registered, the console draws charts and searches logs from it. This page says what you can ask for, what a scoped user is refused, and how time windows behave.

## The browser never sends a query

Other reads delegate authorization to the cluster's own RBAC. A metrics or logs backend knows nothing about Kubernetes identities, so a scoped user handing it a query could read the whole cluster. Here the caller never supplies a query:

- A chart request names an entry from a **fixed catalogue** (below), plus optional namespace, pod and container.
- A log search supplies Kubernetes names plus a free-text filter.

kubemg writes the PromQL, LogsQL or LogQL itself, around the caller's grant.

??? info "Why it works this way"
    An in-cluster datasource's Service usually lives in a namespace, typically `monitoring`, that a scoped grant does not cover. So the hop to the backend is made as cluster-admin, like the probe and discovery. The caller's scope is protected by the query that is built around it, not by the path used to reach the backend. The call is still audited. Adding a chart means adding an entry to the server's catalogue, never a query box in the browser.

## The metrics catalogue

| Kind | Splits by | Namespaced | Description |
|---|---|---|---|
| `pod_cpu` | container | yes | CPU used per container, against the container's own limit |
| `pod_memory` | container | yes | Working set per container |
| `namespace_cpu` | pod | yes | CPU used per pod in a namespace |
| `namespace_memory` | pod | yes | Working set per pod in a namespace |
| `cluster_cpu` | none (one line) | **no** | CPU used across every namespace |
| `cluster_memory` | none (one line) | **no** | Working set across every namespace |
| `cluster_cpu_by_namespace` | namespace | yes | CPU used per namespace |
| `cluster_memory_by_namespace` | namespace | yes | Working set per namespace |
| `pod_restarts` | pod | yes | Container restarts (kube-state-metrics) |
| `containers_not_ready` | pod | yes | Containers reporting not ready (kube-state-metrics) |
| `cpu_throttling` | pod | yes | Share of CFS periods a container was throttled |

- `cluster_cpu` and `cluster_memory` are **refused to a scoped caller**, like any cluster-wide list.
- For every namespaced entry, a scoped caller naming no namespace is answered across their granted namespaces. Naming one outside the grant is refused.
- Values are millicores (CPU), bytes (memory), `count` (restarts, containers) or `ratio` (throttling, a fraction of one).
- Restarts, not-ready containers and throttling are "worse when higher", so the chart colours a rise as a problem. The other entries are neutral.

### Comparison (top-N)

Compare ranks an entry over the window against the window just before it, answering "what is worst, and is it worse than before". `topk` defaults to 5 and is capped at 20. A row with no reading in the previous window shows no previous value rather than zero, since "new" and "was quiet" are different facts.

## Log search

Log search filters the registered logs backend by namespace, pod, container and a free-text `filter`.

- Namespace, pod and container must be legal Kubernetes names. Anything else is refused, never escaped.
- The `filter` text is quoted as a literal, so its characters cannot become query syntax.
- Results are newest first, up to `limit` (default 200, maximum 1000). `limited: true` means "narrow the window", not "there are no more logs".

## The window

| Rule | Value |
|---|---|
| Default span | one hour |
| Maximum span | 30 days |
| Step | derived from the span, never taken from the caller; at most about 500 points per chart |

Widening the range coarsens the resolution instead of enlarging the response. `NaN` and infinite samples are dropped, so a gap shows as a gap and not as zero.

## What the console draws

Every chart has a legend, a crosshair readout, arrow-key navigation and a **table view**, so each value is reachable without a pointer. Past eight series the rest fold into one line.

## Live utilisation (not a series)

Node and pod meters read the cluster's own Metrics API (what `kubectl top` reads) through the ordinary impersonated, audited tunnel. They do not use a datasource.

- Node metrics are cluster-wide and refused to a scoped grant.
- metrics-server is optional. If the cluster answers `404` or `503`, the console shows "not available" with a reason, not an error.
- This is a live sample, not a series: metrics-server keeps about two minutes, so the console draws meters here and never a chart. For longer history, register a [datasource](datasources.md).
