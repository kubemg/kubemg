/*
 * What a list *says*, as opposed to what it contains.
 *
 * A pod list answers "which pods are there"; an operator opening it is almost
 * always asking something coarser first — is anything broken in here, and how
 * much of it. That question is answerable from the rows already in the browser,
 * so it costs no read: these are pure derivations over a loaded list, which is
 * also what keeps them testable and what keeps the header from ever disagreeing
 * with the table under it.
 *
 * Two shapes, because the fleet has two kinds of thing worth summarising this
 * way: pods (phases, readiness, restarts) and the workloads that own them
 * (replicas desired against replicas ready). Services deliberately have no
 * summary — a Service has no health of its own to report, and inventing one
 * from its endpoints would be a claim the list cannot back up.
 */

import type { Pod, Workload } from '../api/types'
import type { Tone } from './status'
import type { PodUsageIndex } from './units'

/** The pod buckets a tile can narrow the list to. */
export type PodBucket =
  | 'all'
  | 'running'
  | 'notready'
  | 'pending'
  | 'failed'
  | 'succeeded'
  | 'unknown'
  | 'restarting'

/** The workload buckets, on the same principle. */
export type WorkloadBucket = 'all' | 'available' | 'degraded' | 'unavailable' | 'scaledtozero'

export type InsightBucket = PodBucket | WorkloadBucket

/**
 * One reading in the header. `tone` is omitted for a reading that is not a
 * state — a total is a count, not a condition, and colouring it would make the
 * one number that is never worrying look like one that might be.
 */
export interface InsightStat {
  id: InsightBucket
  label: string
  value: number
  detail?: string
  tone?: Tone
  /**
   * Whether clicking it narrows the list. A reading that is not a subset of the
   * rows — replicas ready, which counts replicas rather than objects — has
   * nothing to narrow to and is rendered as text rather than as a control.
   */
  selectable: boolean
}

/** One object worth looking at now, named rather than counted. */
export interface InsightAlert {
  key: string
  name: string
  namespace: string
  /** Why it is here, in the cluster's own words where there are any. */
  reason: string
  tone: 'warn' | 'bad'
}

export interface ResourceInsight {
  /** What the whole list is, in one sentence, for the header's own line. */
  headline: string
  stats: InsightStat[]
  alerts: InsightAlert[]
  /** How many objects are in an alerting state, which is more than `alerts` holds. */
  alerting: number
  /** Aggregate live consumption, when the cluster serves the Metrics API. */
  usage?: { cpu: number; memory: number; sampled: number }
}

/**
 * Container states that mean the pod is not going to fix itself. They are the
 * cluster's own words — kubelet writes the waiting reason into the container
 * status and the backend passes it through — so an alert can say
 * `ImagePullBackOff` rather than the useless "not ready", which is the
 * difference between a header worth reading and a header worth hiding.
 */
const CONTAINER_FAILURES = new Set([
  'CrashLoopBackOff',
  'ImagePullBackOff',
  'ErrImagePull',
  'InvalidImageName',
  'CreateContainerConfigError',
  'CreateContainerError',
  'RunContainerError',
  'OOMKilled',
  'Error',
])

/**
 * Restarts worth naming a pod over. A pod that restarted once during a rollout
 * is not news; one that has restarted five times is either crash-looping or
 * being OOM-killed, and both are things somebody should see without asking.
 */
const RESTART_ALERT = 5

/** How many alerts the header names before it starts counting instead. */
export const MAX_ALERTS = 5

/** Which bucket a pod is in. Every pod is in exactly one. */
export function podBucket(pod: Pod): Exclude<PodBucket, 'all' | 'restarting'> {
  switch (pod.phase) {
    case 'Failed':
      return 'failed'
    case 'Succeeded':
      return 'succeeded'
    case 'Pending':
      return 'pending'
    case 'Running':
      // Running is not the same as working: a pod whose readiness probe is
      // failing stays in the Running phase indefinitely, and that is exactly
      // the state a phase-only summary would call healthy.
      return pod.ready === pod.total ? 'running' : 'notready'
    default:
      return 'unknown'
  }
}

/** The failing container state on a pod, or null when nothing names one. */
export function podFailureReason(pod: Pod): string | null {
  for (const container of pod.containers) {
    if (CONTAINER_FAILURES.has(container.state)) return container.state
  }
  return null
}

/**
 * Both matchers take the whole `InsightBucket` union rather than their own half
 * of it, so a caller holding one selection for whichever list is open never has
 * to cast: a workload bucket simply narrows no pods, which is the truthful
 * answer and not an error worth handling.
 */
export function matchesPodBucket(pod: Pod, bucket: InsightBucket): boolean {
  switch (bucket) {
    case 'all':
      return true
    // Restarts cut across the phases rather than partitioning them: a
    // crash-looping pod is Running between restarts.
    case 'restarting':
      return pod.restarts > 0
    case 'running':
    case 'notready':
    case 'pending':
    case 'failed':
    case 'succeeded':
    case 'unknown':
      return podBucket(pod) === bucket
    default:
      return false
  }
}

/** Which bucket a workload is in. Every workload is in exactly one. */
export function workloadBucket(workload: Workload): Exclude<WorkloadBucket, 'all'> {
  // Deliberately scaled to zero is not degraded — it is what somebody asked
  // for, and reporting it as an outage is how a header trains people to
  // ignore it.
  if (workload.desired === 0) return 'scaledtozero'
  if (workload.ready >= workload.desired) return 'available'
  if (workload.ready === 0) return 'unavailable'
  return 'degraded'
}

export function matchesWorkloadBucket(workload: Workload, bucket: InsightBucket): boolean {
  switch (bucket) {
    case 'all':
      return true
    case 'available':
    case 'degraded':
    case 'unavailable':
    case 'scaledtozero':
      return workloadBucket(workload) === bucket
    default:
      return false
  }
}

/** Plural without a lookup table, which is all these labels ever need. */
function plural(count: number, word: string): string {
  return `${count} ${word}${count === 1 ? '' : 's'}`
}

/**
 * Ordering for the named alerts: what cannot recover on its own first, then
 * what is still settling, then what has merely been restarting. Within a rank,
 * the most restarts first — that is the one that has been wrong the longest.
 */
type RankedAlert = InsightAlert & { rank: number; restarts: number }

function rankAlerts(alerts: RankedAlert[]): InsightAlert[] {
  return alerts
    .sort((a, b) => a.rank - b.rank || b.restarts - a.restarts || a.name.localeCompare(b.name))
    .map(({ rank: _rank, restarts: _restarts, ...alert }) => alert)
}

/**
 * podInsights summarises a loaded pod list. Buckets that are empty are left out
 * entirely rather than shown as zeroes: on a healthy namespace the header is
 * two readings wide, and a row of zeroes is noise that makes the one non-zero
 * number harder to find rather than easier.
 */
export function podInsights(pods: Pod[], usage: PodUsageIndex | null): ResourceInsight {
  const counts: Record<Exclude<PodBucket, 'all' | 'restarting'>, number> = {
    running: 0,
    notready: 0,
    pending: 0,
    failed: 0,
    succeeded: 0,
    unknown: 0,
  }

  let restarts = 0
  let restarting = 0
  const alerts: RankedAlert[] = []

  for (const pod of pods) {
    const bucket = podBucket(pod)
    counts[bucket] += 1
    restarts += pod.restarts
    if (pod.restarts > 0) restarting += 1

    const key = `${pod.namespace}/${pod.name}`
    const base = { key, name: pod.name, namespace: pod.namespace, restarts: pod.restarts }
    const failure = podFailureReason(pod)

    if (failure) {
      alerts.push({ ...base, reason: failure, tone: 'bad', rank: 0 })
    } else if (bucket === 'failed') {
      alerts.push({ ...base, reason: 'Failed', tone: 'bad', rank: 0 })
    } else if (bucket === 'unknown') {
      alerts.push({ ...base, reason: pod.phase || 'Unknown', tone: 'warn', rank: 1 })
    } else if (bucket === 'notready') {
      alerts.push({ ...base, reason: `${pod.ready}/${pod.total} ready`, tone: 'warn', rank: 2 })
    } else if (bucket === 'pending') {
      alerts.push({ ...base, reason: 'Pending', tone: 'warn', rank: 2 })
    } else if (pod.restarts >= RESTART_ALERT) {
      alerts.push({ ...base, reason: `${pod.restarts} restarts`, tone: 'warn', rank: 3 })
    }
  }

  const stats: InsightStat[] = [
    { id: 'all', label: 'Pods', value: pods.length, selectable: true },
    { id: 'running', label: 'Running', value: counts.running, tone: 'ok', selectable: true },
  ]

  const optional: Array<[Exclude<PodBucket, 'all' | 'restarting'>, string, Tone]> = [
    ['notready', 'Not ready', 'warn'],
    ['pending', 'Pending', 'warn'],
    ['failed', 'Failed', 'bad'],
    ['unknown', 'Unknown', 'warn'],
    ['succeeded', 'Succeeded', 'idle'],
  ]
  for (const [id, label, tone] of optional) {
    if (counts[id] > 0) stats.push({ id, label, value: counts[id], tone, selectable: true })
  }

  if (restarts > 0) {
    // The value counts restarts and the detail counts pods, because both
    // answer something: one restart across forty pods is a rollout, forty
    // restarts on one pod is a crash loop.
    stats.push({
      id: 'restarting',
      label: 'Restarts',
      value: restarts,
      detail: `across ${plural(restarting, 'pod')}`,
      tone: 'warn',
      selectable: true,
    })
  }

  let aggregate: ResourceInsight['usage']
  if (usage) {
    let cpu = 0
    let memory = 0
    let sampled = 0
    for (const pod of pods) {
      const sample = usage.get(`${pod.namespace}/${pod.name}`)
      if (!sample) continue
      cpu += sample.cpu_millicores
      memory += sample.memory_bytes
      sampled += 1
    }
    if (sampled > 0) aggregate = { cpu, memory, sampled }
  }

  const unhealthy = counts.notready + counts.pending + counts.failed + counts.unknown

  return {
    headline:
      pods.length === 0
        ? 'Nothing running here'
        : unhealthy === 0
          ? `All ${plural(pods.length, 'pod')} are running`
          : `${plural(unhealthy, 'pod')} not running normally`,
    stats,
    alerts: rankAlerts(alerts).slice(0, MAX_ALERTS),
    alerting: alerts.length,
    usage: aggregate,
  }
}

/**
 * workloadInsights summarises Deployments, StatefulSets or DaemonSets — one
 * shape because the three answer the same question, `ready` against `desired`.
 * `label` is the kind's own plural, so the total reads "Deployments" rather
 * than a generic word nobody uses at a terminal.
 */
export function workloadInsights(workloads: Workload[], label: string): ResourceInsight {
  const counts: Record<Exclude<WorkloadBucket, 'all'>, number> = {
    available: 0,
    degraded: 0,
    unavailable: 0,
    scaledtozero: 0,
  }

  let ready = 0
  let desired = 0
  const alerts: RankedAlert[] = []

  for (const workload of workloads) {
    const bucket = workloadBucket(workload)
    counts[bucket] += 1
    ready += workload.ready
    desired += workload.desired

    if (bucket === 'unavailable' || bucket === 'degraded') {
      alerts.push({
        key: `${workload.namespace}/${workload.name}`,
        name: workload.name,
        namespace: workload.namespace,
        reason: `${workload.ready}/${workload.desired} ready`,
        tone: bucket === 'unavailable' ? 'bad' : 'warn',
        rank: bucket === 'unavailable' ? 0 : 1,
        // Nothing to break a tie on but how much of the workload is missing,
        // which is the right order anyway.
        restarts: workload.desired - workload.ready,
      })
    }
  }

  const stats: InsightStat[] = [
    { id: 'all', label, value: workloads.length, selectable: true },
    { id: 'available', label: 'Available', value: counts.available, tone: 'ok', selectable: true },
  ]

  if (counts.degraded > 0) {
    stats.push({
      id: 'degraded',
      label: 'Degraded',
      value: counts.degraded,
      tone: 'warn',
      selectable: true,
    })
  }
  if (counts.unavailable > 0) {
    stats.push({
      id: 'unavailable',
      label: 'Unavailable',
      value: counts.unavailable,
      tone: 'bad',
      selectable: true,
    })
  }
  if (counts.scaledtozero > 0) {
    stats.push({
      id: 'scaledtozero',
      label: 'Scaled to zero',
      value: counts.scaledtozero,
      tone: 'idle',
      selectable: true,
    })
  }

  if (desired > 0) {
    stats.push({
      id: 'all',
      label: 'Replicas ready',
      value: ready,
      detail: `of ${desired} desired`,
      tone: ready === desired ? 'ok' : 'warn',
      // Replicas are not rows: there is no list of them to narrow to.
      selectable: false,
    })
  }

  const failing = counts.degraded + counts.unavailable

  return {
    headline:
      workloads.length === 0
        ? 'Nothing deployed here'
        : failing === 0
          ? `Every workload has the replicas it asked for`
          : `${failing} of ${workloads.length} below their desired replicas`,
    stats,
    alerts: rankAlerts(alerts).slice(0, MAX_ALERTS),
    alerting: alerts.length,
  }
}
