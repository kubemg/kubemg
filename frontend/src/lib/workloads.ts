import type { ResourceKey } from './resources'

/**
 * Which workload kinds answer for which of the two lifecycle controls. It is a
 * table rather than a check at each call site because the two sets are not the
 * same, and the difference is not arbitrary: a DaemonSet has no replica count to
 * set — it runs one pod per node, and the node list is the count.
 *
 * ReplicaSets are missing because they are missing from the Explore inventory:
 * they are a Deployment's bookkeeping rather than something anyone browses. The
 * backend answers for them anyway, since a ReplicaSet does scale and the route
 * should not have to change if a list of them ever appears.
 */
export interface WorkloadCapability {
  scale: boolean
  restart: boolean
}

const WORKLOAD_CAPABILITIES: Partial<Record<ResourceKey, WorkloadCapability>> = {
  deployments: { scale: true, restart: true },
  statefulsets: { scale: true, restart: true },
  daemonsets: { scale: false, restart: true },
}

/**
 * workloadCapability says which actions a resource key answers for, or nothing
 * at all for a kind these controls do not apply to — which is most of them.
 */
export function workloadCapability(key: string): WorkloadCapability | undefined {
  return WORKLOAD_CAPABILITIES[key as ResourceKey]
}

/**
 * The kinds whose logs are their pods' logs, read together.
 *
 * It is a wider set than the two lifecycle controls above, and deliberately so:
 * a DaemonSet cannot be scaled but reading its log across every node is most of
 * why anyone opens one, and a Job's pods are the only place its failure is
 * written down. What every entry has in common is a `spec.selector` the backend
 * can derive the pod set from — which is why a CronJob is absent: it owns Jobs,
 * not pods, and has no selector of its own.
 */
const WORKLOAD_LOG_KINDS: readonly ResourceKey[] = [
  'deployments',
  'statefulsets',
  'daemonsets',
  'jobs',
]

/**
 * supportsWorkloadLogs says whether a kind's detail drawer offers the pooled log
 * view. A pod has its own, which is the per-pod one.
 */
export function supportsWorkloadLogs(key: string): boolean {
  return WORKLOAD_LOG_KINDS.includes(key as ResourceKey)
}

/**
 * workloadKeyFor turns the Kind a workload row carries into the resource key the
 * API is addressed by. The workload table serves three kinds at once, so a row
 * knows what it is in Kubernetes' terms rather than in the sidebar's.
 */
export function workloadKeyFor(kind: string): ResourceKey | undefined {
  const key = `${kind.toLowerCase()}s`
  return workloadCapability(key) ? (key as ResourceKey) : undefined
}
