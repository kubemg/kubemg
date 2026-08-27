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
 * **Every** list in the inventory gets one now, on one shape, because a header
 * that appears on two kinds and vanishes on the other fourteen makes the page
 * jump and teaches nobody where to look. What differs per kind is not the
 * skeleton but what is honest to put in it, and there are two classes:
 *
 *   - kinds with a **state** of their own — pods, the workloads that own them,
 *     Jobs, PVs, PVCs, Nodes, Namespaces, Helm releases, Ingresses, routes —
 *     whose rows carry a phase, a readiness or a bound/unbound, so the header
 *     can say whether anything is wrong;
 *   - kinds that are an **inventory** — Services, StorageClasses, ConfigMaps,
 *     Secrets, CRDs, custom resources — which have no health at all. Those get
 *     a composition (by type, by provisioner, by API group) and a headline that
 *     counts rather than judges. A Service still gets no health reading here,
 *     for the reason it never did: deriving one from endpoints it does not own
 *     would be a claim the list cannot back up.
 *
 * Narrowing follows the same honesty rule. A reading is a control only where
 * there is a matcher behind it — pods and workloads — and everything else is
 * plain text rather than a button that does nothing.
 */

import type {
  ClusterNode,
  ClusterRoleEntry,
  ConfigEntry,
  CronJob,
  CustomResource,
  CustomResourceDefinition,
  HelmRelease,
  HorizontalPodAutoscaler,
  Ingress,
  Job,
  LimitRange,
  Namespace,
  NetworkPolicy,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  PodDisruptionBudget,
  ReplicaSet,
  ResourceQuota,
  RoleBindingEntry,
  Route,
  Service,
  ServiceAccountEntry,
  StorageClass,
  Workload,
} from '../api/types'
import type { Tone } from './status'
import { formatCPU, formatMemory } from './units'
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
   * nothing to narrow to and is rendered as text rather than as a control. So is
   * every reading on a kind with no matcher: see the note at the top.
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

/**
 * One band of the bar: a slice of the total, and the whole point of the
 * distinction this file now draws.
 *
 * A segment is **part of a partition** — every row of the list is in exactly one
 * of them, and they sum to the total. That is what earns a width on a bar and a
 * place in its legend. A reading that is not a partition (keys in a ConfigMap
 * list, restarts across a pod list, replicas against desired) is an
 * `InsightStat` instead: true, useful, and meaningless as a width.
 *
 * The band used to mix the two in one column, which is why it read as
 * arbitrary — "Not ready 2" and "Keys 214" sat in the same list, one a third of
 * the rows and the other a number larger than the total.
 *
 * A segment carries either a `tone` (a state: running, failed, bound) or a
 * `slot` (a composition: a Service type, an API group). `slot` indexes the
 * deck's eight chart tokens, which are a validated colour-blindness set in that
 * order — so a segment carries its slot rather than a colour, and nothing here
 * may reorder them or invent a ninth.
 */
export interface InsightSegment {
  id: InsightBucket
  label: string
  value: number
  detail?: string
  /** Share of the total, 0–1, which is the segment's width on the bar. */
  share: number
  tone?: Tone
  slot?: number
  /** Whether clicking it narrows the list; see the note on `InsightStat`. */
  selectable: boolean
}

export interface ResourceInsight {
  /** What the whole list is, in one sentence, for the header's own line. */
  headline: string
  /** The dot beside that sentence. `idle` is what an inventory reads as. */
  headlineTone: Tone
  /** How many rows there are. Selectable wherever the kind has a matcher, since
      clicking the total is how a narrowing is cleared. */
  total: InsightStat
  /**
   * The partition of that total, empty buckets left out — the bar, and the
   * chips under it. Empty for a kind that has no honest partition at all, and
   * then no bar is drawn rather than one band claiming to be a shape.
   */
  segments: InsightSegment[]
  /** The scalars that are true of the list without being slices of it. */
  readings: InsightStat[]
  alerts: InsightAlert[]
  /** How many objects are in an alerting state, which is more than `alerts` holds. */
  alerting: number
  /** Aggregate live consumption, when the cluster serves the Metrics API. */
  usage?: { cpu: number; memory: number; sampled: number }
  /**
   * The compact mono fragments the folded header carries on its right. Folding
   * has to cost the reader something, but it must not cost them the reason they
   * would have unfolded.
   */
  summary: string[]
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

/**
 * How many bands a composition bar draws. It is the chart palette's length
 * because that palette *is* the colour-blindness mechanism: past eight, the
 * ninth value would either repeat a hue or rest identity on position alone, so
 * the tail folds into one band that says how many it stands for.
 *
 * A state partition is never folded — its buckets are a closed set this file
 * enumerates, and none of them is ever the ninth.
 */
export const MAX_SEGMENTS = 8

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

/**
 * The label a narrowing is named by once it is active, looked up across both
 * reading regions. It ignores a non-selectable reading on purpose: two stats can
 * share a bucket id (a workload's total and its replica count both carry `all`)
 * and only one of them is something a person clicked.
 */
export function bucketLabel(
  insight: ResourceInsight | null,
  bucket: InsightBucket | null,
): string | null {
  if (!insight || !bucket) return null
  for (const stat of [insight.total, ...insight.segments, ...insight.readings]) {
    if (stat.selectable && stat.id === bucket) return stat.label
  }
  return null
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

/** Counts distinct values, dropping the ones a row simply does not have. */
function tally<T>(rows: T[], value: (row: T) => string | undefined | null): Map<string, number> {
  const counts = new Map<string, number>()
  for (const row of rows) {
    const key = value(row)
    if (!key) continue
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return counts
}

/** Most first, then alphabetical, which is the only stable tie-break here. */
function ranked(counts: Map<string, number>): Array<[string, number]> {
  return [...counts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
}

/**
 * A composition: how a list divides along a categorical field it carries — a
 * Service type, an API group, a storage class, a namespace.
 *
 * Drawn only where there is genuinely a spread. One value covering everything is
 * a bar with a single band, which restates the total and earns none of the width
 * it takes, so that returns nothing and the band draws no bar at all.
 *
 * Never selectable: there is no matcher behind an open set of values the cluster
 * named, and a control that does nothing is worse than a number.
 */
function composition<T>(
  rows: T[],
  value: (row: T) => string | undefined | null,
): InsightSegment[] {
  const entries = ranked(tally(rows, value))
  if (entries.length < 2) return []

  const total = entries.reduce((sum, [, count]) => sum + count, 0)
  const named = entries.length > MAX_SEGMENTS ? entries.slice(0, MAX_SEGMENTS - 1) : entries
  const segments: InsightSegment[] = named.map(([key, count], index) => ({
    id: 'all' as const,
    label: key,
    value: count,
    share: count / total,
    slot: index,
    selectable: false,
  }))

  const rest = entries.slice(named.length)
  if (rest.length > 0) {
    const count = rest.reduce((sum, [, n]) => sum + n, 0)
    segments.push({
      id: 'all',
      label: `${rest.length} more`,
      value: count,
      share: count / total,
      slot: MAX_SEGMENTS - 1,
      selectable: false,
    })
  }

  return segments
}

/**
 * The same thing for a kind whose states are an open set the cluster names
 * rather than a closed one this file can enumerate — a PV phase, a Helm status,
 * a Job state. A tone rather than a slot, because these mean something.
 *
 * Ordered by count so the dominant state leads, and never selectable for the
 * same reason a composition is not.
 */
function states<T>(
  rows: T[],
  value: (row: T) => string,
  tone: (state: string) => Tone,
): InsightSegment[] {
  const entries = ranked(tally(rows, value))
  const total = entries.reduce((sum, [, count]) => sum + count, 0)
  return entries.map(([state, count]) => ({
    id: 'all' as const,
    label: state,
    value: count,
    share: total > 0 ? count / total : 0,
    tone: tone(state),
    selectable: false,
  }))
}

/**
 * One band of a closed state partition — the buckets this file does enumerate,
 * and the only segments that are ever selectable. The total is passed in rather
 * than derived, because a share is of the whole list and not of whichever
 * buckets happened to come out non-empty.
 */
function segment(
  id: InsightBucket,
  label: string,
  value: number,
  total: number,
  tone: Tone,
  options: { detail?: string; selectable?: boolean } = {},
): InsightSegment {
  return {
    id,
    label,
    value,
    share: total > 0 ? value / total : 0,
    tone,
    detail: options.detail,
    selectable: options.selectable ?? false,
  }
}

/** A plain reading with no state behind it. */
function reading(label: string, value: number, detail?: string, tone?: Tone): InsightStat {
  return { id: 'all', label, value, detail, tone, selectable: false }
}

/**
 * The shared skeleton for an inventory kind: a total, a composition, a headline
 * that counts. Everything below either calls this or is a state kind with enough
 * of its own logic to be worth writing out.
 */
function inventory(
  label: string,
  count: number,
  segments: InsightSegment[],
  readings: InsightStat[],
  headline: string,
  summary: string[],
): ResourceInsight {
  return {
    headline,
    headlineTone: 'idle',
    total: reading(label, count),
    segments,
    readings,
    alerts: [],
    alerting: 0,
    summary,
  }
}

/** The empty answer, which every builder short-circuits to. */
function nothing(label: string, headline: string): ResourceInsight {
  return {
    headline,
    headlineTone: 'idle',
    total: reading(label, 0),
    segments: [],
    readings: [],
    alerts: [],
    alerting: 0,
    summary: [],
  }
}

/**
 * podInsights summarises a loaded pod list. Buckets that are empty are left out
 * entirely rather than shown as zeroes: on a healthy namespace the header is
 * two readings deep, and a column of zeroes is noise that makes the one non-zero
 * number harder to find rather than easier.
 */
export function podInsights(
  pods: Pod[],
  usage: PodUsageIndex | null,
  label = 'Pods',
): ResourceInsight {
  if (pods.length === 0) return nothing(label, 'Nothing running here')

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

  // The partition, healthy band first so the bar reads left to right from
  // "fine" to "not". Empty buckets are left out entirely: on a healthy namespace
  // the bar is one band, not six with five of them zero-width.
  const segments: InsightSegment[] = []
  const buckets: Array<[Exclude<PodBucket, 'all' | 'restarting'>, string, Tone]> = [
    ['running', 'Running', 'ok'],
    ['notready', 'Not ready', 'warn'],
    ['pending', 'Pending', 'warn'],
    ['failed', 'Failed', 'bad'],
    ['unknown', 'Unknown', 'warn'],
    ['succeeded', 'Succeeded', 'idle'],
  ]
  for (const [id, name, tone] of buckets) {
    if (counts[id] > 0) {
      segments.push(segment(id, name, counts[id], pods.length, tone, { selectable: true }))
    }
  }

  const readings: InsightStat[] = []
  if (restarts > 0) {
    // Not a segment: a pod that has restarted is still Running, so this crosses
    // the partition rather than dividing it. The value counts restarts and the
    // detail counts pods, because both answer something — one restart across
    // forty pods is a rollout, forty restarts on one pod is a crash loop.
    readings.push({
      id: 'restarting',
      label: 'restarts',
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
  const summary: string[] = []
  if (aggregate) summary.push(`CPU ${formatCPU(aggregate.cpu)}`, `MEM ${formatMemory(aggregate.memory)}`)
  if (restarts > 0) summary.push(plural(restarts, 'restart'))

  return {
    headline:
      unhealthy === 0
        ? `All ${plural(pods.length, 'pod')} are running`
        : `${plural(unhealthy, 'pod')} not running normally`,
    headlineTone: unhealthy === 0 ? (restarts > 0 ? 'warn' : 'ok') : counts.failed > 0 ? 'bad' : 'warn',
    total: { id: 'all', label, value: pods.length, selectable: true },
    segments,
    readings,
    alerts: rankAlerts(alerts).slice(0, MAX_ALERTS),
    alerting: alerts.length,
    usage: aggregate,
    summary,
  }
}

/**
 * workloadInsights summarises Deployments, StatefulSets or DaemonSets — one
 * shape because the three answer the same question, `ready` against `desired`.
 * `label` is the kind's own plural, so the total reads "Deployments" rather
 * than a generic word nobody uses at a terminal.
 */
export function workloadInsights(workloads: Workload[], label: string): ResourceInsight {
  if (workloads.length === 0) return nothing(label, 'Nothing deployed here')

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

  const segments: InsightSegment[] = []
  const buckets: Array<[Exclude<WorkloadBucket, 'all'>, string, Tone]> = [
    ['available', 'Available', 'ok'],
    ['degraded', 'Degraded', 'warn'],
    ['unavailable', 'Unavailable', 'bad'],
    ['scaledtozero', 'Scaled to zero', 'idle'],
  ]
  for (const [id, name, tone] of buckets) {
    if (counts[id] > 0) {
      segments.push(segment(id, name, counts[id], workloads.length, tone, { selectable: true }))
    }
  }

  const readings: InsightStat[] = []
  if (desired > 0) {
    readings.push({
      id: 'all',
      label: 'replicas ready',
      value: ready,
      detail: `of ${desired} desired`,
      tone: ready === desired ? 'ok' : 'warn',
      // Replicas are not rows: there is no list of them to narrow to, and they
      // outnumber the objects, so they were never a slice of this total.
      selectable: false,
    })
  }

  const failing = counts.degraded + counts.unavailable

  return {
    headline:
      failing === 0
        ? 'Every workload has the replicas it asked for'
        : `${failing} of ${workloads.length} below their desired replicas`,
    headlineTone: failing === 0 ? 'ok' : counts.unavailable > 0 ? 'bad' : 'warn',
    total: { id: 'all', label, value: workloads.length, selectable: true },
    segments,
    readings,
    alerts: rankAlerts(alerts).slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: desired > 0 ? [`${ready}/${desired} replicas`] : [],
  }
}

/**
 * A Job's state is the cluster's own: the backend resolves it from the
 * `Complete`/`Failed` conditions, falling back to Suspended/Running/Pending. A
 * failed Job is the one thing here worth naming rather than counting, because a
 * Job that failed does not retry itself past its backoff limit.
 */
export function jobInsights(jobs: Job[], label: string): ResourceInsight {
  if (jobs.length === 0) return nothing(label, 'No jobs here')

  const complete = jobs.filter((job) => job.state === 'Complete').length
  const failedJobs = jobs.filter((job) => job.state === 'Failed' || job.failed > 0)
  const running = jobs.filter((job) => job.active > 0).length

  const alerts = rankAlerts(
    failedJobs.map((job) => ({
      key: `${job.namespace}/${job.name}`,
      name: job.name,
      namespace: job.namespace,
      reason: job.state === 'Failed' ? 'Failed' : `${job.failed} failed`,
      tone: 'bad' as const,
      rank: 0,
      restarts: job.failed,
    })),
  )

  return {
    headline:
      failedJobs.length === 0
        ? `${complete} of ${jobs.length} complete, none failed`
        : `${plural(failedJobs.length, 'job')} failed`,
    headlineTone: failedJobs.length === 0 ? 'ok' : 'bad',
    total: reading(label, jobs.length),
    segments: states(jobs, (job) => job.state || 'Unknown', jobStateTone),
    // Jobs with a pod in flight, which cuts across Running and Suspended alike.
    readings: running > 0 ? [reading('active', running, 'with a pod in flight', 'accent')] : [],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: failedJobs.length > 0 ? [`${failedJobs.length} failed`] : [`${complete} complete`],
  }
}

function jobStateTone(state: string): Tone {
  if (state === 'Complete') return 'ok'
  if (state === 'Failed') return 'bad'
  if (state === 'Suspended') return 'idle'
  return 'warn'
}

/**
 * A CronJob has no health — it has a schedule. What is worth reading off the
 * list is how much of it is suspended, since a suspended CronJob looks identical
 * to a working one in every column but that flag.
 */
export function cronJobInsights(cronjobs: CronJob[], label: string): ResourceInsight {
  if (cronjobs.length === 0) return nothing(label, 'No cron jobs here')

  const suspended = cronjobs.filter((job) => job.suspended).length
  const running = cronjobs.reduce((sum, job) => sum + job.active, 0)
  const active = cronjobs.length - suspended

  return {
    headline:
      suspended === 0
        ? `All ${plural(cronjobs.length, 'schedule')} are active`
        : `${suspended} of ${cronjobs.length} suspended`,
    headlineTone: suspended === 0 ? 'ok' : 'warn',
    total: reading(label, cronjobs.length),
    segments: [
      segment('all', 'Active', active, cronjobs.length, 'ok'),
      ...(suspended > 0
        ? [segment('all', 'Suspended', suspended, cronjobs.length, 'idle')]
        : []),
    ],
    // Jobs, not schedules: one CronJob can have several in flight at once.
    readings: running > 0 ? [reading('running now', running, 'jobs in flight', 'accent')] : [],
    alerts: [],
    alerting: 0,
    summary: suspended > 0 ? [`${suspended} suspended`] : [`${running} in flight`],
  }
}

/**
 * A Service is an inventory: it has no state of its own, and the only honest
 * composition is by type — which is also the one an operator scans for, since a
 * LoadBalancer costs money and a NodePort is a hole.
 */
export function serviceInsights(services: Service[], label: string): ResourceInsight {
  if (services.length === 0) return nothing(label, 'No services here')

  const exposed = services.filter(
    (service) => service.type === 'LoadBalancer' || service.type === 'NodePort',
  ).length

  return inventory(
    label,
    services.length,
    states(services, (service) => service.type || 'Unknown', serviceTypeTone),
    [],
    exposed === 0
      ? `${plural(services.length, 'service')}, none exposed outside the cluster`
      : `${exposed} of ${services.length} exposed outside the cluster`,
    exposed > 0 ? [`${exposed} exposed`] : [],
  )
}

function serviceTypeTone(type: string): Tone {
  // Not health — reach. LoadBalancer and NodePort leave the cluster, and that is
  // worth the eye landing on it; nothing here is claiming one is broken.
  if (type === 'LoadBalancer' || type === 'NodePort') return 'accent'
  return 'idle'
}

/**
 * An Ingress does have a state, and it is one the list already carries: an
 * Ingress its controller has not given an address to is not routing anything,
 * which every column but `addresses` hides.
 */
export function ingressInsights(ingresses: Ingress[], label: string): ResourceInsight {
  if (ingresses.length === 0) return nothing(label, 'No ingresses here')

  const pending = ingresses.filter((ingress) => ingress.addresses.length === 0)
  const hosts = new Set(ingresses.flatMap((ingress) => ingress.hosts)).size

  const alerts = rankAlerts(
    pending.map((ingress) => ({
      key: `${ingress.namespace}/${ingress.name}`,
      name: ingress.name,
      namespace: ingress.namespace,
      reason: 'no address yet',
      tone: 'warn' as const,
      rank: 0,
      restarts: 0,
    })),
  )

  return {
    headline:
      pending.length === 0
        ? `All ${plural(ingresses.length, 'ingress')} have an address`
        : `${plural(pending.length, 'ingress')} without an address`,
    headlineTone: pending.length === 0 ? 'ok' : 'warn',
    total: reading(label, ingresses.length),
    segments: [
      segment('all', 'Addressed', ingresses.length - pending.length, ingresses.length, 'ok'),
      ...(pending.length > 0
        ? [segment('all', 'No address', pending.length, ingresses.length, 'warn')]
        : []),
    ],
    readings: [
      reading('hosts', hosts),
      reading(
        'rules',
        ingresses.reduce((sum, ingress) => sum + ingress.rules, 0),
      ),
    ],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [plural(hosts, 'host')],
  }
}

/**
 * A NetworkPolicy list has no working/broken state of its own — a policy is
 * either there or it is not, and whether it is doing the right thing is what
 * the reachability tab answers, not this header. So this reads as an
 * inventory: how many policies, which directions they govern, and how many
 * select every pod in their namespace (an empty `podSelector`), which is worth
 * naming because it is easy to write by accident and hard to spot in a list of
 * peer rules.
 */
export function networkPolicyInsights(policies: NetworkPolicy[], label: string): ResourceInsight {
  if (policies.length === 0) return nothing(label, 'No NetworkPolicies here')

  const both = policies.filter(
    (policy) => policy.policy_types.includes('Ingress') && policy.policy_types.includes('Egress'),
  ).length
  const ingressOnly = policies.filter(
    (policy) => policy.policy_types.includes('Ingress') && !policy.policy_types.includes('Egress'),
  ).length
  const egressOnly = policies.filter(
    (policy) => policy.policy_types.includes('Egress') && !policy.policy_types.includes('Ingress'),
  ).length
  const selectsEveryPod = policies.filter((policy) => policy.pod_selector === '').length

  return inventory(
    label,
    policies.length,
    // Which directions a policy governs *is* a partition — a policy is one of
    // the three — so it is the bar. Selecting every pod is not: it cuts across
    // all three, and it is the one worth a tone.
    [
      ...(both > 0 ? [segment('all', 'Both directions', both, policies.length, 'idle')] : []),
      ...(ingressOnly > 0
        ? [segment('all', 'Ingress only', ingressOnly, policies.length, 'idle')]
        : []),
      ...(egressOnly > 0
        ? [segment('all', 'Egress only', egressOnly, policies.length, 'idle')]
        : []),
    ],
    selectsEveryPod > 0
      ? [reading('select every pod', selectsEveryPod, 'empty podSelector', 'idle')]
      : [],
    `${policies.length} ${policies.length === 1 ? 'NetworkPolicy' : 'NetworkPolicies'} here`,
    [],
  )
}

/**
 * A route — an HTTPRoute or a VirtualService — is bound to its gateway or not,
 * and an unattached one is configuration nothing is serving.
 */
export function routeInsights(routes: Route[], label: string): ResourceInsight {
  if (routes.length === 0) return nothing(label, 'No routes here')

  const orphaned = routes.filter((route) => route.parents.length === 0)
  const hosts = new Set(routes.flatMap((route) => route.hostnames)).size

  const alerts = rankAlerts(
    orphaned.map((route) => ({
      key: `${route.namespace}/${route.name}`,
      name: route.name,
      namespace: route.namespace,
      reason: 'no gateway attached',
      tone: 'warn' as const,
      rank: 0,
      restarts: 0,
    })),
  )

  return {
    headline:
      orphaned.length === 0
        ? `All ${plural(routes.length, 'route')} are attached to a gateway`
        : `${plural(orphaned.length, 'route')} attached to no gateway`,
    headlineTone: orphaned.length === 0 ? 'ok' : 'warn',
    total: reading(label, routes.length),
    segments: [
      segment('all', 'Attached', routes.length - orphaned.length, routes.length, 'ok'),
      ...(orphaned.length > 0
        ? [segment('all', 'Unattached', orphaned.length, routes.length, 'warn')]
        : []),
    ],
    readings: [
      reading('hostnames', hosts),
      reading(
        'rules',
        routes.reduce((sum, route) => sum + route.rules, 0),
      ),
    ],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [plural(hosts, 'hostname')],
  }
}

/** A release is deployed, failed, or somewhere in between — its own word for it. */
export function helmInsights(releases: HelmRelease[], label: string): ResourceInsight {
  if (releases.length === 0) return nothing(label, 'No Helm releases here')

  const deployed = releases.filter((release) => release.status === 'deployed').length
  const broken = releases.filter((release) => helmStatusTone(release.status) === 'bad')

  const alerts = rankAlerts(
    broken.map((release) => ({
      key: `${release.namespace}/${release.name}`,
      name: release.name,
      namespace: release.namespace,
      reason: release.status,
      tone: 'bad' as const,
      rank: 0,
      restarts: 0,
    })),
  )

  return {
    headline:
      broken.length === 0
        ? `All ${plural(releases.length, 'release')} are deployed`
        : `${plural(broken.length, 'release')} in a failed state`,
    headlineTone: broken.length === 0 ? 'ok' : 'bad',
    total: reading(label, releases.length),
    segments: states(releases, (release) => release.status || 'unknown', helmStatusTone),
    // Distinct charts, which is not a slice: several releases can share one.
    readings: [reading('charts', new Set(releases.map((release) => release.chart_name)).size)],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: broken.length > 0 ? [`${broken.length} failed`] : [`${deployed} deployed`],
  }
}

function helmStatusTone(status: string): Tone {
  if (status === 'deployed') return 'ok'
  if (status === 'failed' || status === 'unknown') return 'bad'
  if (status === 'superseded' || status === 'uninstalled') return 'idle'
  return 'warn'
}

/** A volume's phase is a state: Released or Failed is storage nothing can claim. */
export function volumeInsights(volumes: PersistentVolume[], label: string): ResourceInsight {
  if (volumes.length === 0) return nothing(label, 'No persistent volumes here')

  const bound = volumes.filter((volume) => volume.status === 'Bound').length
  const stranded = volumes.filter((volume) => volumeStatusTone(volume.status) !== 'ok')

  const alerts = rankAlerts(
    stranded
      .filter((volume) => volume.status !== 'Available')
      .map((volume) => ({
        key: volume.name,
        name: volume.name,
        // A PV is cluster-scoped: there is no namespace to name, and the drawer
        // it opens does not want one either.
        namespace: '',
        reason: volume.status,
        tone: volume.status === 'Failed' ? ('bad' as const) : ('warn' as const),
        rank: volume.status === 'Failed' ? 0 : 1,
        restarts: 0,
      })),
  )

  return {
    headline:
      bound === volumes.length
        ? `All ${plural(volumes.length, 'volume')} are bound`
        : `${bound} of ${volumes.length} bound`,
    headlineTone: alerts.length > 0 ? 'warn' : bound === volumes.length ? 'ok' : 'idle',
    total: reading(label, volumes.length),
    segments: states(volumes, (volume) => volume.status || 'Unknown', volumeStatusTone),
    readings: [
      reading('storage classes', new Set(volumes.map((volume) => volume.storage_class || 'none')).size),
    ],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${bound}/${volumes.length} bound`],
  }
}

function volumeStatusTone(status: string): Tone {
  if (status === 'Bound') return 'ok'
  if (status === 'Failed') return 'bad'
  if (status === 'Available') return 'idle'
  return 'warn'
}

/** A claim that is Pending is a pod that will not start, so it gets named. */
export function claimInsights(claims: PersistentVolumeClaim[], label: string): ResourceInsight {
  if (claims.length === 0) return nothing(label, 'No claims here')

  const bound = claims.filter((claim) => claim.status === 'Bound').length
  const waiting = claims.filter((claim) => claim.status !== 'Bound')

  const alerts = rankAlerts(
    waiting.map((claim) => ({
      key: `${claim.namespace}/${claim.name}`,
      name: claim.name,
      namespace: claim.namespace,
      reason: claim.status || 'Unknown',
      tone: claim.status === 'Lost' ? ('bad' as const) : ('warn' as const),
      rank: claim.status === 'Lost' ? 0 : 1,
      restarts: 0,
    })),
  )

  return {
    headline:
      waiting.length === 0
        ? `All ${plural(claims.length, 'claim')} are bound`
        : `${plural(waiting.length, 'claim')} not bound`,
    headlineTone: waiting.length === 0 ? 'ok' : 'warn',
    total: reading(label, claims.length),
    segments: states(claims, (claim) => claim.status || 'Unknown', volumeStatusTone),
    readings: [
      reading('storage classes', new Set(claims.map((claim) => claim.storage_class || 'none')).size),
    ],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${bound}/${claims.length} bound`],
  }
}

/** Which provisioner backs the cluster's storage, and whether one is the default. */
/**
 * ReplicaSets. Almost every row is at zero desired — that is what a superseded
 * ReplicaSet looks like — so the reading worth putting up is the opposite one:
 * how many are actually carrying pods, and whether any of them wants pods it
 * has not got. A namespace mid-rollout has exactly one of the latter.
 */
export function replicaSetInsights(replicasets: ReplicaSet[], label: string): ResourceInsight {
  if (replicasets.length === 0) return nothing(label, 'No ReplicaSets here')

  const active = replicasets.filter((entry) => entry.desired > 0)
  const short = active.filter((entry) => entry.ready < entry.desired).length

  return inventory(
    label,
    replicasets.length,
    [
      ...(active.length > 0
        ? [segment('all', 'Active', active.length, replicasets.length, 'ok')]
        : []),
      ...(replicasets.length - active.length > 0
        ? [
            segment(
              'all',
              'Scaled to zero',
              replicasets.length - active.length,
              replicasets.length,
              'idle',
            ),
          ]
        : []),
    ],
    short > 0 ? [reading('short of pods', short, undefined, 'warn')] : [],
    short > 0
      ? `${plural(short, 'ReplicaSet')} short of the pods it wants`
      : `${plural(replicasets.length, 'ReplicaSet')}, ${active.length} carrying pods`,
    short > 0 ? [`${short} short`] : [],
  )
}

/**
 * HorizontalPodAutoscalers. The one thing worth calling out is an autoscaler
 * that cannot read its metric: it looks exactly like a quiet one on any table
 * of replica counts, and it means the workload is not being scaled at all. The
 * second is an autoscaler pinned at its ceiling, which is where the next
 * traffic spike has nowhere left to go.
 */
export function autoscalerInsights(
  autoscalers: HorizontalPodAutoscaler[],
  label: string,
): ResourceInsight {
  if (autoscalers.length === 0) return nothing(label, 'No autoscalers here')

  const broken = autoscalers.filter((entry) => !!entry.reason).length
  const atCeiling = autoscalers.filter(
    (entry) => !entry.reason && entry.current_replicas >= entry.max_replicas,
  ).length

  return inventory(
    label,
    autoscalers.length,
    [
      ...(broken > 0 ? [segment('all', 'Not scaling', broken, autoscalers.length, 'bad')] : []),
      ...(atCeiling > 0
        ? [segment('all', 'At ceiling', atCeiling, autoscalers.length, 'warn')]
        : []),
      ...(autoscalers.length - broken - atCeiling > 0
        ? [
            segment(
              'all',
              'Scaling',
              autoscalers.length - broken - atCeiling,
              autoscalers.length,
              'ok',
            ),
          ]
        : []),
    ],
    [
      ...(broken > 0 ? [reading('cannot read metrics', broken, undefined, 'bad')] : []),
      ...(atCeiling > 0 ? [reading('at max replicas', atCeiling, undefined, 'warn')] : []),
    ],
    broken > 0
      ? `${plural(broken, 'autoscaler')} cannot read its metrics`
      : `${plural(autoscalers.length, 'autoscaler')} here`,
    broken > 0 ? [`${broken} not scaling`] : [],
  )
}

/**
 * ResourceQuotas. The header answers the question the list is opened with —
 * "is anything full" — by counting the entries at or over their hard limit.
 * Quantities are compared as text where they are equal and not otherwise: two
 * Kubernetes quantities are only reliably comparable through a unit parser this
 * derivation deliberately does not have, so "full" here means "used equals
 * hard", which is exact, rather than a percentage that would sometimes be wrong.
 */
export function quotaInsights(quotas: ResourceQuota[], label: string): ResourceInsight {
  if (quotas.length === 0) return nothing(label, 'No quotas here')

  const entries = quotas.flatMap((quota) => quota.entries)
  const exhausted = entries.filter((entry) => !!entry.used && entry.used === entry.hard).length

  return inventory(
    label,
    quotas.length,
    [],
    [
      reading('bounded resources', entries.length),
      ...(exhausted > 0 ? [reading('at their limit', exhausted, undefined, 'bad')] : []),
    ],
    exhausted > 0
      ? `${plural(exhausted, 'resource')} at its hard limit — nothing more will schedule`
      : `${plural(quotas.length, 'quota')} bounding ${plural(entries.length, 'resource')}`,
    exhausted > 0 ? [`${exhausted} exhausted`] : [],
  )
}

/** LimitRanges. An inventory kind: what matters is which types are bounded. */
export function limitRangeInsights(ranges: LimitRange[], label: string): ResourceInsight {
  if (ranges.length === 0) return nothing(label, 'No LimitRanges here')

  const entries = ranges.flatMap((range) => range.entries)
  return inventory(
    label,
    ranges.length,
    composition(entries, (entry) => entry.type),
    [reading('bounds', entries.length)],
    `${plural(ranges.length, 'LimitRange')} declaring ${plural(entries.length, 'bound')}`,
    [],
  )
}

/**
 * PodDisruptionBudgets. Two things are worth the eye: a budget allowing no
 * disruption at all, which is what a drain hangs on, and a budget that selects
 * nothing, which protects nothing while looking like protection.
 */
export function disruptionBudgetInsights(
  budgets: PodDisruptionBudget[],
  label: string,
): ResourceInsight {
  if (budgets.length === 0) return nothing(label, 'No disruption budgets here')

  const blocking = budgets.filter((entry) => entry.disruptions_allowed === 0).length
  const selectsNothing = budgets.filter((entry) => !entry.selector).length

  return inventory(
    label,
    budgets.length,
    [
      ...(blocking > 0
        ? [segment('all', 'Allowing none', blocking, budgets.length, 'warn')]
        : []),
      ...(budgets.length - blocking > 0
        ? [segment('all', 'Allowing disruption', budgets.length - blocking, budgets.length, 'ok')]
        : []),
    ],
    selectsNothing > 0
      ? [reading('select no pods', selectsNothing, 'empty selector', 'bad')]
      : [],
    blocking > 0
      ? `${plural(blocking, 'budget')} allowing no disruption — a drain will block here`
      : `${plural(budgets.length, 'budget')}, all allowing disruption`,
    blocking > 0 ? [`${blocking} blocking`] : [],
  )
}

export function storageClassInsights(classes: StorageClass[], label: string): ResourceInsight {
  if (classes.length === 0) return nothing(label, 'No storage classes here')

  const defaults = classes.filter((entry) => entry.default).length

  return inventory(
    label,
    classes.length,
    // Which provisioner backs each class is the partition worth seeing; the
    // binding mode is a second one, and a bar can only be one.
    composition(classes, (entry) => entry.provisioner),
    [
      // No default class means a claim with no class named stays Pending
      // forever, which is worth the eye landing on.
      reading('default', defaults, undefined, defaults === 1 ? 'ok' : 'warn'),
      reading('provisioners', new Set(classes.map((entry) => entry.provisioner)).size),
    ],
    defaults === 1
      ? `${plural(classes.length, 'class')}, one of them the default`
      : defaults === 0
        ? `${plural(classes.length, 'class')} and no default — an unnamed class will not bind`
        : `${defaults} classes are marked default, which is one too many`,
    defaults === 1 ? [] : [`${defaults} default`],
  )
}

/**
 * ConfigMaps and Secrets are an inventory of keys. The count of keys is the
 * reading that means something — no value ever leaves the cluster, so there is
 * nothing else here to summarise.
 */
export function configInsights(entries: ConfigEntry[], label: string, secrets: boolean) {
  if (entries.length === 0) return nothing(label, secrets ? 'No secrets here' : 'No config here')

  const keys = entries.reduce((sum, entry) => sum + entry.keys.length, 0)
  const immutable = entries.filter((entry) => entry.immutable).length

  return inventory(
    label,
    entries.length,
    // A Secret's type is a real partition and a useful one — a TLS secret and a
    // Helm release record are not the same object. A ConfigMap has no such
    // field, so it gets no bar rather than a bar of namespaces, which the
    // namespace scope above the list already answers.
    secrets ? composition(entries, (entry) => entry.type || 'Opaque') : [],
    [
      reading('keys', keys),
      ...(immutable > 0 ? [reading('immutable', immutable, undefined, 'idle')] : []),
    ],
    `${plural(entries.length, secrets ? 'secret' : 'config map')} holding ${plural(keys, 'key')}`,
    [plural(keys, 'key')],
  )
}

/** What this cluster has been extended with, grouped by whose extension it is. */
export function crdInsights(crds: CustomResourceDefinition[], label: string): ResourceInsight {
  if (crds.length === 0) return nothing(label, 'This cluster has no custom resources')

  const namespaced = crds.filter((crd) => crd.scope === 'Namespaced').length
  const groups = new Set(crds.map((crd) => crd.group)).size

  return inventory(
    label,
    crds.length,
    // Whose extension it is, which is the question a CRD list is opened with.
    composition(crds, (crd) => crd.group),
    [
      reading('namespaced', namespaced),
      reading('cluster-scoped', crds.length - namespaced),
      reading('API groups', groups),
    ],
    `${plural(crds.length, 'definition')} across ${plural(groups, 'API group')}`,
    [plural(groups, 'group')],
  )
}

/**
 * A custom resource's spec is whatever its author decided, so there is nothing
 * to derive a state from. What is honest is how many there are and where.
 */
export function customInsights(rows: CustomResource[], label: string): ResourceInsight {
  if (rows.length === 0) return nothing(label, 'None of these on this cluster')

  return inventory(
    label,
    rows.length,
    // One list can hold several kinds when it came from a discovered group.
    composition(rows, (row) => row.kind),
    [reading('kinds', new Set(rows.map((row) => row.kind).filter(Boolean)).size)],
    `${plural(rows.length, 'object')} on this cluster`,
    [],
  )
}

/**
 * A node's readiness is the one state in the inventory an operator is woken up
 * for, and cordoned is the one that is deliberate but invisible in every other
 * column.
 */
export function nodeInsights(nodes: ClusterNode[], label: string): ResourceInsight {
  if (nodes.length === 0) return nothing(label, 'No nodes here')

  const ready = nodes.filter((node) => node.ready).length
  const cordoned = nodes.filter((node) => node.unschedulable).length
  const down = nodes.filter((node) => !node.ready)

  const alerts = rankAlerts(
    down.map((node) => ({
      key: node.name,
      name: node.name,
      namespace: '',
      reason: node.status || 'NotReady',
      tone: 'bad' as const,
      rank: 0,
      restarts: 0,
    })),
  )

  return {
    headline:
      down.length === 0
        ? cordoned === 0
          ? `All ${plural(nodes.length, 'node')} are ready and schedulable`
          : `${plural(nodes.length, 'node')} ready, ${cordoned} cordoned`
        : `${plural(down.length, 'node')} not ready`,
    headlineTone: down.length > 0 ? 'bad' : cordoned > 0 ? 'warn' : 'ok',
    total: reading(label, nodes.length),
    segments: [
      segment('all', 'Ready', ready, nodes.length, 'ok'),
      ...(down.length > 0 ? [segment('all', 'Not ready', down.length, nodes.length, 'bad')] : []),
    ],
    readings: [
      // Cordoned is not a slice: a cordoned node is usually Ready as well, and
      // that overlap is the whole reason it is worth saying out loud.
      ...(cordoned > 0 ? [reading('cordoned', cordoned, 'unschedulable', 'warn')] : []),
      reading('kubelet versions', new Set(nodes.map((node) => node.version)).size),
      reading('roles', new Set(nodes.map((node) => node.roles[0] ?? 'none')).size),
    ],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${ready}/${nodes.length} ready`],
  }
}

/*
 * The RBAC headers.
 *
 * These are inventory kinds — a Role has no health, and inventing one would be a
 * claim the list cannot back up. But they are not *only* inventory, because one
 * property of an RBAC list is genuinely worth an operator's eye landing on it:
 * how much of it grants everything. A wildcard rule is how a role that reads as
 * narrow turns out not to be, and it is invisible in a table of names.
 *
 * So the wildcard count is toned, and nothing else is. It is deliberately not an
 * *alert* — a wildcard role is not a fault, it is a fact about the cluster
 * (`cluster-admin` is one, and every cluster has it), and naming a handful of
 * them as things to go and look at would be a header crying wolf on a fresh
 * install. What is honest is the count and the ability to see it at a glance.
 */

/** A Role or ClusterRole list, summarised by how much it grants. */
export function roleInsights(
  roles: ClusterRoleEntry[],
  label: string,
  clusterScoped: boolean,
): ResourceInsight {
  if (roles.length === 0) return nothing(label, `No ${label.toLowerCase()} visible to you`)

  const wildcard = roles.filter((role) => role.wildcard).length
  // Kubernetes' own roles are most of a fresh cluster's ClusterRole list, so
  // separating them is what makes the remainder — the ones somebody here wrote —
  // legible at all.
  const builtin = roles.filter((role) => role.builtin).length
  const authored = roles.length - builtin
  const rules = roles.reduce((sum, role) => sum + role.rule_count, 0)

  return inventory(
    label,
    roles.length,
    // Who wrote them is the partition that makes the list legible: Kubernetes'
    // own roles are most of a fresh cluster's, and the remainder is what
    // somebody here actually authored.
    builtin > 0 && authored > 0
      ? [
          segment('all', 'Authored here', authored, roles.length, 'accent'),
          segment('all', "Kubernetes' own", builtin, roles.length, 'idle'),
        ]
      : [],
    [
      ...(wildcard > 0
        ? [reading('grant * on something', wildcard, undefined, 'warn')]
        : []),
      reading('rules', rules),
      // A ClusterRole has no namespace, so the shape worth counting is the
      // policy's: how much of the API surface these roles touch between them.
      clusterScoped
        ? reading(
            'resources touched',
            new Set(roles.flatMap((role) => role.resources)).size,
          )
        : reading('namespaces', new Set(roles.map((role) => role.namespace ?? '-')).size),
    ],
    wildcard > 0
      ? `${plural(roles.length, 'role')}, ${wildcard} granting * on something`
      : `${plural(authored, 'role')}${builtin > 0 ? ` beyond Kubernetes' own ${builtin}` : ''}`,
    wildcard > 0 ? [`${wildcard} wildcard`] : [],
  )
}

/**
 * A binding list, summarised by blast radius. The reading that matters is how
 * many bindings reach every namespace at once and how many reach a subject that
 * is a group — a group binding is one line of YAML that can grant a hundred
 * people something, which is the shape an audit is looking for.
 */
export function bindingInsights(
  bindings: RoleBindingEntry[],
  label: string,
  clusterScoped: boolean,
): ResourceInsight {
  if (bindings.length === 0) return nothing(label, `No ${label.toLowerCase()} visible to you`)

  const subjects = bindings.reduce((sum, binding) => sum + binding.subject_count, 0)
  const groups = bindings.filter((binding) => binding.kinds?.includes('Group')).length
  // A RoleBinding pointing at a ClusterRole is the construct people misread
  // most: it applies that ClusterRole's rules *inside one namespace*, and
  // counting them is the cheapest way to see how much of a namespace's access
  // is defined somewhere else entirely.
  const viaClusterRole = bindings.filter((binding) => binding.role_kind === 'ClusterRole').length
  const empty = bindings.filter((binding) => binding.subject_count === 0).length

  return inventory(
    label,
    bindings.length,
    // Which role each binding points at: the partition an audit reads first,
    // because it is how "who can do this" is actually grouped.
    composition(bindings, (binding) => binding.role_name),
    [
      reading('subjects', subjects),
      ...(groups > 0 ? [reading('bind a group', groups)] : []),
      ...(!clusterScoped && viaClusterRole > 0
        ? [reading('use a ClusterRole', viaClusterRole, 'applied in-namespace')]
        : []),
      // A binding with no subjects grants nothing. It is not a fault, but it is
      // almost always a leftover, and it never shows up in a list of names.
      ...(empty > 0 ? [reading('bind nobody', empty, undefined, 'idle')] : []),
    ],
    clusterScoped
      ? `${plural(bindings.length, 'cluster-wide binding')} reaching ${plural(subjects, 'subject')}`
      : `${plural(bindings.length, 'binding')} reaching ${plural(subjects, 'subject')}`,
    [plural(subjects, 'subject')],
  )
}

/**
 * ServiceAccounts. The reading worth having is how many are `default` — that is
 * one per namespace whether anyone wanted it, and anything bound to it is bound
 * to every workload in that namespace that never named an account.
 */
export function serviceAccountInsights(
  accounts: ServiceAccountEntry[],
  label: string,
): ResourceInsight {
  if (accounts.length === 0) return nothing(label, 'No service accounts visible to you')

  const defaults = accounts.filter((account) => account.default).length
  const declined = accounts.filter((account) => account.automount_token === false).length

  return inventory(
    label,
    accounts.length,
    // The `default` account exists whether anyone wanted it, so the split that
    // matters is it against the accounts somebody created on purpose.
    defaults > 0 && accounts.length > defaults
      ? [
          segment('all', 'Created here', accounts.length - defaults, accounts.length, 'accent'),
          segment('all', 'default', defaults, accounts.length, 'idle', {
            detail: 'one per namespace',
          }),
        ]
      : [],
    declined > 0 ? [reading('token declined', declined, 'not automounted', 'idle')] : [],
    `${plural(accounts.length, 'account')} workloads here can run as`,
    [],
  )
}

/**
 * A namespace stuck Terminating is a finalizer nobody has cleared, and it is
 * invisible in a list that only prints names.
 */
export function namespaceInsights(namespaces: Namespace[], label: string): ResourceInsight {
  if (namespaces.length === 0) return nothing(label, 'No namespaces visible to you')

  const active = namespaces.filter((entry) => entry.status === 'Active').length
  const granted = namespaces.filter((entry) => entry.granted).length
  const terminating = namespaces.filter((entry) => entry.status === 'Terminating')

  const alerts = rankAlerts(
    terminating.map((entry) => ({
      key: entry.name,
      name: entry.name,
      namespace: '',
      reason: 'Terminating',
      tone: 'warn' as const,
      rank: 0,
      restarts: 0,
    })),
  )

  return {
    headline:
      terminating.length === 0
        ? `${plural(namespaces.length, 'namespace')}, all active`
        : `${plural(terminating.length, 'namespace')} still terminating`,
    headlineTone: terminating.length === 0 ? 'ok' : 'warn',
    total: reading(label, namespaces.length),
    segments: [
      segment('all', 'Active', active, namespaces.length, 'ok'),
      ...(terminating.length > 0
        ? [segment('all', 'Terminating', terminating.length, namespaces.length, 'warn')]
        : []),
    ],
    readings: [
      // What the grant actually covers, which is the question a namespace list
      // is usually opened to answer. Not a slice: a granted namespace is also
      // an active one.
      reading('granted to you', granted, granted === namespaces.length ? 'all of them' : undefined),
    ],
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${granted} granted`],
  }
}
