# Node capacity

The capacity page (`/clusters/:id/capacity`) shows how much of each node is reserved, how much is used, and why a pod will not schedule. A node at 30% CPU can still refuse a pod, because the scheduler places work against **requests**, not live usage.

## Three numbers

For every node and for the whole cluster, three figures are measured against **allocatable** (capacity minus what the kubelet and system reserve):

| Figure | Source | Meaning |
|---|---|---|
| **Requested** | Pod specs | What the scheduler has already promised |
| **Limited** | Pod specs | The ceiling if everything on the node spent its limit |
| **Used** | `metrics.k8s.io` (optional) | What is spent right now |

Requested and limited use the scheduler's own arithmetic: regular containers plus native sidecars, the larger of that total and the peak of any init step, plus pod overhead. A plain init container's own limit is ignored.

A fourth ceiling can bind first: **pod slots**, the kubelet's cap on pod count, regardless of CPU or memory.

??? info "Why it works this way"
    Getting sidecars wrong understates every node running a service mesh. A finished init step constrains nothing in steady state, so its limit is skipped.

## Heatmap

Below the cluster totals, the **Heatmap** puts every node on one row and every ceiling in one cell: CPU reserved, in use and limits; memory reserved, in use and limits; pod slots. Beside them sit the node's unreserved CPU and memory, and its pods by QoS class (Guaranteed, Burstable, BestEffort, as the cluster wrote them). A fleet of two hundred nodes fits on one screen, and the bars further down explain whichever row you open.

- **Rows** are grouped by role, control plane first, and start sorted worst first. Click a column heading to sort by it (fullest first), or **Node** to sort by name. Nodes that cannot answer for a column always sort last.
- **Shade** darkens toward the column's warning line. Amber means past it, rust means exhausted. The lines are the ones the concerns below use: reserved and slots 90% (100% exhausted), memory in use 90%, CPU limits 200%, memory limits 100%. **CPU in use never turns amber**, because a node short of CPU still gives every pod what it requested.
- **The number is the reading.** A limit of 340% reads 340%.
- **A dash is "not measured"**, never 0%. You see one for live usage when the Metrics API has no reading, and for any column on a node that reports no allocatable capacity.
- **Click a node's name** to open its row below, with the bars, every concern, taints, the largest reservations and the pods using more than they reserved.

## Where a pod can still go

The cluster card ends with the fragmentation reading: the unreserved CPU and memory summed across every node an ordinary pod could land on, and the most any **one** node can take. Twelve free cores spread one per node do not schedule a two-core pod, and the total alone would say they do.

A node counts as able to take an ordinary pod when it is Ready, not cordoned, has a free pod slot, and carries no `NoSchedule` or `NoExecute` taint (`PreferNoSchedule` does not keep a pod off). Node selectors, affinity, topology spread and tolerations are **not** read, so a pod that tolerates a control-plane taint, or one pinned by affinity, can land somewhere this reading leaves out.

## Pods using more than they reserved

When the cluster serves the Metrics API, each node lists the pods whose live use is above their request. A BestEffort pod requests nothing, so everything it uses counts. They are ranked by how much they take beyond their request as a share of the **node**, so a pod at ten times a tiny request does not outrank one 2 GiB over on an 8 GiB node. The list shows the largest five, and the count beside it is exact.

These are the noisy neighbours. When memory runs out, the kubelet evicts BestEffort pods first, then the pods furthest over their request. When CPU runs out, they slow down the pods beside them.

Reading per-pod usage needs `get`/`list` on `pods.metrics.k8s.io`. If the cluster refuses that read, the page says so in the cluster's own words and shows everything else. The borrower lists are then empty: they are not an "all clear".

## Bars

- The bar fills to the **requested** percentage, with one tick for live usage. The gap between them is the point of the page.
- **Limits are stated in text, never drawn.** Limits routinely exceed allocatable, and a bar would misreport exactly that.
- A reading with no limit to measure against is drawn as a **hatch**, not a full bar.

## Routes

| Route | Answers |
|---|---|
| `GET .../metrics/capacity` | Full report: allocatable, requested, limited, used per node; taints, placeability, headroom, QoS split and the pods using more than they reserved; a fleet summary with the placement reading; per-node concerns; unplaceable pods |
| `GET .../metrics/nodes` | Live node usage (`kubectl top nodes`) against allocatable |
| `GET .../metrics/pods[?all_namespaces=true]` | Live pod usage, scoped like any namespaced list |
| `GET .../metrics/pods/:pod` | One pod, with container breakdown |

`/metrics/nodes` and `/metrics/capacity` say nothing about one namespace, so they are **refused to a namespace-scoped grant**. Only `/metrics/pods` fans out per granted namespace.

## metrics-server is optional

If the Metrics API is missing (404) or its backend is down (503), that means "no metrics right now", not a failure. Capacity still answers fully from pod specs and reports `available: false` with the reason.

## Concerns and severity

Each node is reduced to **concerns** with a `code`, a `severity` (`ok`/`note`/`warn`/`danger`) and a sentence written server-side.

| Concern | Severity |
|---|---|
| Node not Ready | `danger` |
| Cordoned | `warn` |
| CPU or memory fully / nearly reserved | `danger` / `warn` |
| CPU limits past 200% of the node, or memory limits past 100% | `warn` |
| Pod slots exhausted or nearly so | raised as a concern |
| Memory in use at 90% or more of allocatable (only when live usage is available) | `warn` |
| CPU in use at 90% or more of allocatable (only when live usage is available) | `note` |
| Resources reserved but mostly unspent (only when live usage is available) | `note` |
| Containers with no limit at all | `note` |

Thresholds (90% committed, 200%/100% overcommit, 50%/50% reserved-idle) are fixed, not settings. CPU is throttled under pressure; memory is not, so a node whose memory limits exceed its size answers a spike by evicting a pod.

Pods the scheduler could not place are listed separately (a sample of 10), each with the scheduler's own `PodScheduled=False` message, for example "0/5 nodes are available: 5 Insufficient memory".

## Fleet overview

The Overview's **Fleet capacity** card makes one call per attached cluster. It is capped at 12 clusters, and the page says so past the cap.

Nothing on this page costs money, recommends a size or changes the cluster. All reads go through the same impersonated, audited tunnel.

## See also

- [Metrics and logs](../observability/metrics-and-logs.md) for history beyond the live sample.
- [Cluster actions](actions.md) for scaling a workload once a node's numbers explain why it will not schedule.
