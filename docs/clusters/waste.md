# Waste

The waste page (`/clusters/:id/waste`) shows what a cluster reserves and does not use. The [capacity page](capacity.md) can tell you a node is mostly reserved and half idle. This page tells you whose reservation that is, and what to change. It looks at three things:

| Section | What it lists | Needs |
|---|---|---|
| **Over-requested workloads** | Deployments, StatefulSets and DaemonSets whose containers request well past what they used | A [metrics datasource](../observability/datasources.md) |
| **Storage nothing uses** | Claims no running pod mounts, and volumes left released or never bound | The cluster alone |
| **Idle load balancers** | `type: LoadBalancer` Services with no ready endpoint behind them | The cluster alone |

The page **recommends and never writes**. It has **no money** on it: what can be given back is stated in cores and bytes, because a price depends on your contract, not on the cluster.

## Over-requested workloads

For every running Deployment, StatefulSet and DaemonSet, each container's request is compared with what it used over the window you choose (**1d**, **7d**, **14d** or **30d**, default 7d):

- **CPU** is the 95th percentile of the container's five-minute CPU rate. CPU can be shared: a container over its request is slowed down, not killed. So a request should cover what the container normally needs, not its rarest spike.
- **Memory** is the container's peak working set. Memory cannot be shared that way: a container that needs more than its node can give is the one that gets evicted. So the only safe basis is the most it ever held.

Each workload is measured by its **busiest replica**, since a request has to cover every replica. Native sidecars are included, but plain init containers are not, because they finish and stop using anything.

A request is **over-requested** when it is more than **1.5×** that reading. The suggested request is the reading plus **20%**, rounded up (CPU to 5m, memory to 1 MiB) and never below **10m** of CPU or **32 MiB** of memory. A workload is listed only when it would give back at least **100m** of CPU or **128 MiB** of memory across its replicas. Below that, nearly every small Deployment would qualify and the list would be noise.

Some workloads are checked but not listed:

- **Less than a day of history:** a day covers one daily cycle, and anything shorter is a guess, so these workloads are counted and not sized. History starts at the oldest running pod's start or at the first sample the datasource holds for it, whichever is later. A month-old pod on a datasource registered an hour ago has an hour of history.
- **No usage recorded:** the datasource has no series for the workload's containers, so its reading is missing, not zero.
- **A container with no request** is never called over-requested. It is asking for nothing.

Jobs, CronJobs and bare pods are left out. A Job's request is sized for one run, and a bare pod has no template to change.

When the list is empty, the page says how many workloads it checked and how many it could not size. An empty list is not the same as a clean cluster.

### The suggested change

Open a workload to see each container's request, what it used and the suggested value. A dash means "not declared" or "not measured", and hovering it says which. **keep** means that request is fine.

Below the table is the change as a strategic-merge patch. It changes **only** the requests found over-requested; limits and every other field stay as they are. A sidecar is patched under `initContainers`. To apply it by hand, save it as `rightsize.yaml` and run the command shown, for example:

```bash
kubectl -n shop patch deployment web --patch-file rightsize.yaml
```

If Argo CD, Flux or Helm manages the workload, make the change in its source instead. A patch applied to the cluster is put back at the next sync.

If the pods of a workload disagree about a request, a rollout is in progress. The page says so, shows the newest pod's request, and still counts the older pods' usage.

### Without a metrics datasource

The live Metrics API keeps only the last few minutes, which is not a basis for a request. Without a metrics datasource, this section says so and links an administrator to the cluster's dashboard to register one. The other two sections are answered anyway.

A datasource whose series carry no `container` label can size a single-container pod only. A pod with several containers is counted as not measured, and the page explains why.

## Storage nothing uses

- **Not mounted:** a bound claim that no running pod mounts. When the claim's name follows a StatefulSet's pattern (`<template>-<statefulset>-<ordinal>`), the page names the StatefulSet. If the ordinal is past its replicas, the claim was **kept by a scale-down** on purpose and is reattached if the StatefulSet scales back up. Deleting the claim deletes that data.
- **Released:** a PersistentVolume whose claim was deleted while the data was kept (reclaim policy `Retain`). The page names the claim it belonged to.
- **Never bound:** a PersistentVolume that is `Available` and bound to no claim.

PersistentVolumes are cluster-scoped. If your access is limited to some namespaces, they are not shown, and the page says so. Unmounted claims in your namespaces still are. If the cluster refuses the PersistentVolume list, the page shows the cluster's own reason.

## Idle load balancers

A `type: LoadBalancer` Service is idle when none of its EndpointSlices has a ready endpoint. A cloud load balancer then sits in front of nothing. The state says whether the Service has **no ready endpoint** (its pods are missing or failing) or **no selector** (nothing manages its endpoints). Its external addresses are listed so you can find it with your cloud provider.

If the cluster refuses the EndpointSlice list, no load balancer is called idle, and the page shows the cluster's own reason.

## Access

The page is open to anyone granted the cluster. Every read goes through the agent tunnel as you, so the cluster's own permissions apply and each read lands in the [audit trail](../audit/trail.md). A namespace-scoped grant is answered over its own namespaces, including the usage history. A grant covering more than 25 namespaces is asked to narrow down.

The page is not refreshed automatically. A 30-day history read is the most expensive question kubemg asks a metrics backend, so it runs when the page opens or when you press **Refresh**. **Where these numbers come from**, at the bottom of the page, shows the three queries that were asked: CPU, memory, and when each container's history starts.
