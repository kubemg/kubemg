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
  /**
   * Whether the schedule can be turned off. It is the CronJob's alone and it is
   * the *only* control a CronJob has: it owns Jobs rather than pods, so there
   * is no replica count to set and no template to roll, and deleting it to stop
   * tonight's run would lose the object.
   */
  suspend: boolean
}

const WORKLOAD_CAPABILITIES: Partial<Record<ResourceKey, WorkloadCapability>> = {
  deployments: { scale: true, restart: true, suspend: false },
  statefulsets: { scale: true, restart: true, suspend: false },
  daemonsets: { scale: false, restart: true, suspend: false },
  cronjobs: { scale: false, restart: false, suspend: true },
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
 * The kinds whose pods can be listed — "what is this thing running right now,
 * and is it healthy" — which is a wider question than the pooled log view
 * answers and the one reason to add CronJob here on its own. A CronJob owns no
 * pods directly and answers for none of the other workload questions (it has no
 * `spec.selector`, so it is absent from `WORKLOAD_LOG_KINDS` and from
 * `workloadPodKinds` on the backend), but the pods its Jobs are running *right
 * now* — one, none, or several under `concurrencyPolicy: Allow` — are exactly
 * what "is core-worker-cronjob's last run still going, and is it healthy"
 * means, so the backend resolves it through its Jobs instead of a selector.
 */
const WORKLOAD_POD_KINDS: readonly ResourceKey[] = [...WORKLOAD_LOG_KINDS, 'cronjobs']

/**
 * supportsWorkloadPods says whether a kind's detail drawer offers the Pods tab
 * — the list of pods it owns, with each one's health and live resource usage.
 */
export function supportsWorkloadPods(key: string): boolean {
  return WORKLOAD_POD_KINDS.includes(key as ResourceKey)
}

/**
 * The kinds a NetworkPolicy reachability question can be asked about — the
 * kinds that carry pod labels, which is what a `podSelector` actually matches
 * against. It is `WORKLOAD_LOG_KINDS` plus Pods rather than a copy of it,
 * because the two questions ("what are this workload's pods" and "what labels
 * does this workload's pods carry") land on the same four apps/batch kinds for
 * the same reason: both need a real `spec.template`, which is why CronJob is
 * absent from both — it owns Jobs, not pods, and has no template of its own.
 *
 * Pods are the one addition, and deliberately not folded into the same table:
 * a Pod carries its labels directly on its own metadata, while every other
 * kind here carries them on a pod *template* — the labels a policy will
 * actually see belong to the pod that template produces, which can drift from
 * it after the fact. `hasPodLabels` only says the tab applies; the honesty
 * about which case a given answer is lives in the reachability response
 * itself (`label_source`), not in this predicate.
 */
const POD_LABEL_KINDS: readonly ResourceKey[] = ['pods', ...WORKLOAD_LOG_KINDS]

/** hasPodLabels says whether a kind's detail drawer offers the Reachability tab. */
export function hasPodLabels(key: string): boolean {
  return POD_LABEL_KINDS.includes(key as ResourceKey)
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

