# Node capacity

`GET /api/v1/clusters/:id/metrics/capacity` and the console page at
`/clusters/:id/capacity` answer a question live utilisation cannot: "there is
plenty of room and nothing will schedule." A node sitting at 30% CPU can
still refuse the next pod, because the scheduler places work against
**requests** — a reservation nobody is obliged to spend — not against what is
actually being used right now.

## Three numbers, never the same number

For every node, and for the cluster as a whole, the page reports three
figures against the same **allocatable** denominator (not capacity — what the
kubelet and the system reserve for themselves is not headroom anyone can
schedule into):

| Figure | Source | Meaning |
|---|---|---|
| **Requested** | pod specs | what the scheduler has already promised away |
| **Limited** | pod specs | the ceiling the node would have to honour if everything on it spent its limit |
| **Used** | `metrics.k8s.io`, optional | what is actually being spent right now |

Requested and limited are computed with the **scheduler's own arithmetic**,
not a sum of containers:

- every regular container's request/limit, plus every **native sidecar**
  (an init container with `restartPolicy: Always`, which starts during
  initialisation and keeps running for the pod's life)
- the largest of (a) that running total and (b) the highest any single
  ordinary init step needed *on top of* the sidecars already started before
  it — a pod's actual peak reservation is whichever of those two is larger
- pod overhead (what a sandboxed runtime charges for the sandbox itself),
  added to both
- a plain (non-sidecar) init container's own **limit** is deliberately
  ignored — it constrains a step that has already finished, not the pod's
  steady state

Getting sidecars wrong understates every node running a service mesh, which
is most of them once one is installed.

There is a fourth ceiling, and it binds before the other two do: **pod
slots** — the kubelet's own limit on how many pods it will run, independent
of CPU or memory. A node can have CPU and memory to spare and still refuse a
pod because its pod-slot allocatable is exhausted.

## Routes

- `GET .../metrics/nodes` — live node **usage** only (`kubectl top nodes`),
  paired against allocatable for a percentage. Cluster-wide.
- `GET .../metrics/pods[?all_namespaces=true]` — live pod usage, scoped like
  every other namespaced list.
- `GET .../metrics/pods/:pod` — one pod's usage, container breakdown
  included; used by the pod drawer rather than pulling a whole namespace to
  find one row.
- `GET .../metrics/capacity` — the full report: allocatable, requested,
  limited and used for every node, a fleet-level summary, per-node
  **concerns** with a severity, and the list of pods the scheduler could not
  place at all.

## Cluster-wide, and refused to a scoped grant

Node capacity — like node metrics — says nothing about a namespace and
reaches well past one, so both `/metrics/nodes` and `/metrics/capacity`
refuse a namespace-scoped grant outright (`requireClusterScope`) rather than
answering a partial or misleading view of it. `/metrics/pods` is the only one
of the four that fans out per granted namespace for a scoped caller.

## metrics-server is optional

Usage — the third number — comes from the cluster's own aggregated Metrics
API. It is either installed or it is not: a 404 means the APIService is not
registered, a 503 means it is registered but its backend is down, and from
the caller's side both are treated as "no metrics right now," never as a
failure (`fetchMetrics`). `/metrics/capacity` still answers fully without it
— requested and limited are read straight from pod specs — and reports
`available: false` with `capacityUsageUnavailableReason` naming exactly what
is missing, rather than failing the whole page over a component nobody
installed.

## Concerns and severity

Every node's numbers are reduced to a fixed set of **concerns**, hardest
first, each carrying a `code`, a `severity` (`ok`/`note`/`warn`/`danger`) and
a human sentence written server-side — not assembled in the browser, on the
same reasoning the posture findings follow: a claim about a cluster is one
that must not drift from the arithmetic that produced it. Among them: node
not Ready (`danger`), cordoned (`warn`), CPU/memory fully or nearly fully
reserved (`danger`/`warn`), CPU limits past 200% of the node or memory limits
past 100% (`warn` — CPU is throttled under pressure, memory is not: a node
whose memory limits exceed its size answers a spike by evicting somebody),
pod slots exhausted or nearly so, resources reserved but mostly unspent
(`note` — a FinOps right-sizing signal, only raised when live usage is
actually available), and containers declaring no limit at all (`note`). The
thresholds (90% committed, 200%/100% overcommit, 50%/50% reserved-idle) are
fixed constants, not settings — a threshold an operator can raise until the
warning stops is a threshold that stops meaning anything.

Pods the scheduler could not place at all (`spec.nodeName` empty) are
reported separately as `unscheduled`/`unscheduled_pods`, capped at a sample
of 10, each carrying the scheduler's own `PodScheduled=False` condition
message — "0/5 nodes are available: 5 Insufficient memory" says more about
why than any arithmetic here could invent.

## Fleet overview capacity fan-out

The Overview page's fleet cards each show a capacity row, which is the one
place this console fans a read out across the whole fleet rather than
reading one cluster at a time — one tunnel round trip per attached cluster.
That fan-out is capped at `FLEET_METRICS_LIMIT` (12) attached clusters, and
the page says so past the cap, rather than turning the landing page into N
uncapped round trips on every load.

## Reading the meters

Every bar has the same rule: **a reading with no limit to measure against
renders as a hatch, not a full bar** — a full-width bar reads as "at
capacity," which is the opposite of "unknown," so an unbounded reading is
drawn as a hatch pattern instead of being clamped to 100%. This is the shared
`Meter` primitive's contract (used for a container's own usage-against-limit
in the pod drawer), and the node capacity page's own three-number bar follows
the same spirit while going further: **limits are stated in text, never
drawn**, because a node's limits routinely *exceed* its allocatable — that is
what overcommitment means — and a bar that ran off the end of its track, or
was silently clamped to it, would misreport by exactly the amount that
matters. The bar itself fills to the **requested** percentage (because
requested is what decides scheduling) with a single tick mark showing where
live usage actually sits — a second overlapping fill would read as one bar of
ambiguous length, and the gap between the fill and the tick is the whole
point of the page.

!!! note
    Nothing on this page costs anything, recommends a size, or touches the
    cluster. Every figure is read through the same impersonated, audited
    tunnel as any other list.

## See also

- [Metrics and logs](../observability/metrics-and-logs.md) for history beyond
  the live sample this page and `/metrics/*` provide.
- [Cluster actions](actions.md) for scaling a workload once a node's own
  numbers explain why it will not.
