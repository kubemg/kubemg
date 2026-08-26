# Metrics and logs

## The browser never sends a query

Every other read in kubemg delegates authorization to the target cluster:
the call goes down the tunnel impersonated as the caller, and the cluster's
own RBAC answers it. That does not work for a metrics or logs backend — it
has never heard of the caller, has no notion of a Kubernetes identity, and
will answer whatever it is asked. If a namespace-scoped user could hand a
query straight to Prometheus, one line of PromQL would read the entire
cluster's series.

So this is the one read in kubemg where a caller **never supplies a query
string**. Instead:

- a chart request names a **kind from a fixed catalogue** (below), plus
  optional namespace, pod and container names;
- a log search supplies a **set of Kubernetes names** (namespace, pod,
  container) plus a free-text filter.

The server resolves a `Scope` from the caller's grant and writes the
PromQL/LogsQL/LogQL around it. Enforcement happens once, here, rather than
being delegated — which is also why adding a new chart means adding a line
of hand-written PromQL to the catalogue, not adding a text box to the
browser.

!!! note "The transport is a separate decision"
    An in-cluster datasource's Service usually lives in a namespace (typically
    `monitoring`) a scoped grant does not cover. Asserting the caller's own
    scope on *that* hop would mean no scoped user could ever see a chart — so
    the hop itself is made as cluster-admin, the same way the probe and
    discovery calls are. What protects the caller's scope is the query being
    built around it, not the path used to reach the backend. The call is
    still audited either way.

## The metrics catalogue

`GET /api/v1/clusters/:id/observability/metrics/query?metric=...` answers
one catalogue entry. Every entry is a hand-written PromQL template — that is
the whole point, since a template written by someone who knows what scope
it has to respect is the enforcement mechanism.

| Kind | Splits by | Namespaced | Description |
|---|---|---|---|
| `pod_cpu` | container | yes | CPU used per container, against the container's own limit |
| `pod_memory` | container | yes | Working set per container |
| `namespace_cpu` | pod | yes | CPU used per pod in a namespace |
| `namespace_memory` | pod | yes | Working set per pod in a namespace |
| `cluster_cpu` | — (one line) | **no** | CPU used across every namespace |
| `cluster_memory` | — (one line) | **no** | Working set across every namespace |
| `cluster_cpu_by_namespace` | namespace | yes | CPU used per namespace |
| `cluster_memory_by_namespace` | namespace | yes | Working set per namespace |
| `pod_restarts` | pod | yes | Container restarts (kube-state-metrics) |
| `containers_not_ready` | pod | yes | Containers reporting not ready (kube-state-metrics) |
| `cpu_throttling` | pod | yes | Share of CFS periods a container was throttled |

The two `cluster_*` entries (with no `_by_namespace` suffix) are **refused
outright to a scoped caller** — a cluster-wide reading reaches past a
namespace scope by definition, the same rule a cluster-wide resource list
follows. Every other entry is namespaced: a scoped caller naming no
namespace is answered across their own granted namespaces, and naming one
outside their grant is refused.

Values come back as **millicores** (CPU) or **bytes** (memory) — the same
two units the live Metrics API endpoints normalise to, so a chart and a
meter agree — plus `count` (restarts, containers) and `ratio` (throttling,
sent as a fraction of one rather than a pre-rounded percentage).

Each entry also carries a `Trend`: most readings are `neutral` (a rise is
neither good nor bad), but restarts, not-ready containers, and throttling are
`worse` — a rise there is what the chart's delta colouring is allowed to
react to.

Adding a chart means adding a catalogue entry in
`backend/pkg/observability/metrics_query.go` — a PromQL template written by
someone who knows the scope it has to respect — never a query field exposed
to the browser.

### Comparison (top-N)

`GET .../observability/metrics/compare?metric=...&topk=N` ranks a catalogue
entry over the requested window and against the window immediately before
it, answering "what is worst right now, and is it worse than it was" rather
than "what shape is this". Ranking happens once, as an instant query over
the whole span — `topk` inside a *range* query would let the membership
change mid-window, showing lines that appear and vanish. `topk` defaults to
5 and is capped at 20; past that it stops being a comparison and becomes a
listing, which the resource browser already is. Every row that has no
reading in the previous window is reported as having no `previous` value
rather than a previous reading of zero — "new in this window" and "was quiet"
are different facts.

## Log search

`GET /api/v1/clusters/:id/observability/logs/query` searches the registered
logs backend by namespace, pod, container and a free-text `filter`.

Kubernetes names (namespace, pod, container) are **validated against an
anchored RFC 1123 pattern, never escaped** — a value that is not a legal
Kubernetes name is refused outright, so nothing a caller sends can become
label-matcher or filter syntax. `filter` is the one exception: it is the
caller's own free text, and the whole point of it is to hold arbitrary
characters, so it is **quoted as a literal** into the query rather than
validated (backslash escaped first, so the escaping cannot re-escape its own
output). This is the one place a caller's characters reach a query language.

Results come back newest first — a search is asked what happened, and the
answer starts with the most recent thing that did, unlike the pod log
view's tail which reads forward. A page is capped at `limit` (default 200,
maximum 1000); hitting the cap sets `limited: true`, meaning "narrow the
window", not "no more logs exist".

## The window

Both engines take a `Window` with a `Start`, `End` and derived `Step`. A
caller naming neither boundary gets the default (one hour). The **span is
capped at 30 days** — long enough for a real incident, short enough that a
mistyped range cannot ask a backend for its entire retention — and this
same 30-day figure is the widest an "all time" quick range can honestly
resolve to, since a datasource has its own retention behind that.

The **step is always derived from the span, never taken from the caller**: a
chart can draw at most ~500 points, so widening the range coarsens the
resolution instead of enlarging the response. The step also has a floor —
below the backend's usual scrape interval a finer step invents nothing, it
just repeats samples.

`NaN` and `±Inf` samples are **dropped**, not rendered as zero: Prometheus
writes them for a gap or a staleness marker, and either would fail the whole
JSON decode if it reached the wire as a bare value. A dropped sample becomes
an absent point on the chart, which is what a gap should look like.

## What the console draws

`MetricsChart.tsx` is hand-drawn SVG rather than a charting library — the
smallest one worth having is still heavier than the lazy-loaded terminal.
Every chart ships with a legend, a crosshair readout, arrow-key navigation,
and a **table view** so every value a hover would show is reachable without
a pointer. Series colour comes from the deck's one categorical palette
(`--chart-1` through `--chart-8`); past eight series the rest fold into one
line rather than repeating a colour. `InsightTrend` draws the same reading
as a compact band elsewhere in Explore — one catalogue, two geometries, never
two ways to *read* a series.

## Live utilisation (not a series)

`GET /api/v1/clusters/:id/metrics/nodes`, `.../metrics/pods`, and
`.../metrics/pods/:pod` read the cluster's own aggregated Metrics API
(`metrics.k8s.io`, the same thing `kubectl top` reads) through the ordinary
impersonated, audited tunnel call — not through a registered datasource at
all. Node metrics are cluster-wide and refused to a scoped grant the same
way a cluster-wide resource list is; pod metrics fan out per namespace like
every other namespaced list.

metrics-server is optional. `fetchMetrics` treats **404 and 503** as
`available: false` with a stated `reason` rather than as an error — the same
contract the optional-CRD lists use — because "this cluster does not serve
that" is a legitimate answer.

This is deliberately a **live sample, not a series**: metrics-server keeps
only about two minutes of history, which is why the console draws meters
here and never a chart. A chart over a longer window needs a registered
[datasource](datasources.md) — that is the entire reason the query path
above exists.

Kubernetes quantities (`250m`, `1Gi`, `128974848`) are parsed through
`k8s.io/apimachinery/pkg/api/resource` rather than a hand-rolled parser —
`1Gi` and `1G` differ by about 7%, and getting that wrong would misdraw
every meter in the console.
