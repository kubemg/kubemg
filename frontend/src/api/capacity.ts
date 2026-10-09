import type {
  CapacityDimension,
  CapacitySummary,
  ClusterCapacity,
  NodeCapacity,
  PodSlots,
  QOSCounts,
} from './types'

/*
 * The capacity report, completed field by field before the page sees it.
 *
 * The page reads deep into every node — `node.taints.length`,
 * `summary.placement.largest_cpu` — and one missing array there is not a
 * missing cell, it is a white screen. A missing field happens whenever the
 * console and the server it talks to are not the same build: a dev stack whose
 * backend was not restarted, or a browser holding the previous bundle across an
 * upgrade. So every field the server added after the first version of this
 * report is defaulted here, to the value that reads as "not measured" rather
 * than as a reading: no taints, no QoS, no borrowers, no placeable node.
 *
 * `placeable` defaults to false, not true. A server that does not say whether a
 * node takes ordinary pods has not said it does, and the placement reading must
 * not count it.
 */

const EMPTY_DIMENSION: CapacityDimension = {
  allocatable: 0,
  requested: 0,
  limited: 0,
  used: 0,
  requested_percent: 0,
  limited_percent: 0,
  used_percent: 0,
  unlimited_containers: 0,
}

const EMPTY_SLOTS: PodSlots = { allocatable: 0, scheduled: 0, percent: 0, without_requests: 0 }

const EMPTY_QOS: QOSCounts = { guaranteed: 0, burstable: 0, best_effort: 0 }

function completeNode(node: Partial<NodeCapacity> & { name: string }): NodeCapacity {
  return {
    roles: [],
    ready: false,
    schedulable: false,
    placeable: false,
    severity: 'ok',
    borrowing_pods: 0,
    ...node,
    taints: node.taints ?? [],
    cpu: node.cpu ?? EMPTY_DIMENSION,
    memory: node.memory ?? EMPTY_DIMENSION,
    pods: node.pods ?? EMPTY_SLOTS,
    qos: node.qos ?? EMPTY_QOS,
    headroom: node.headroom ?? { cpu: 0, memory: 0, pods: 0 },
    concerns: node.concerns ?? [],
    top_requests: node.top_requests ?? [],
    top_borrowers: node.top_borrowers ?? [],
  }
}

function completeSummary(summary: Partial<CapacitySummary> | undefined): CapacitySummary {
  return {
    nodes: 0,
    ready: 0,
    schedulable: 0,
    ...summary,
    cpu: summary?.cpu ?? EMPTY_DIMENSION,
    memory: summary?.memory ?? EMPTY_DIMENSION,
    pods: summary?.pods ?? EMPTY_SLOTS,
    qos: summary?.qos ?? EMPTY_QOS,
    severity_counts: summary?.severity_counts ?? {},
    placement: summary?.placement ?? {
      placeable_nodes: 0,
      free: { cpu: 0, memory: 0, pods: 0 },
      largest_cpu: null,
      largest_memory: null,
    },
  }
}

/** completeCapacity fills every field the page reads, so a partial report draws as one. */
export function completeCapacity(data: Partial<ClusterCapacity>): ClusterCapacity {
  return {
    available: data.available ?? false,
    reason: data.reason,
    pod_usage_available: data.pod_usage_available ?? false,
    pod_usage_reason: data.pod_usage_reason,
    nodes: (data.nodes ?? []).map(completeNode),
    summary: completeSummary(data.summary),
    unscheduled: data.unscheduled ?? [],
    unscheduled_pods: data.unscheduled_pods ?? 0,
  }
}
