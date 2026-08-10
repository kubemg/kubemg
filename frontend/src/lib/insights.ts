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
  ConfigEntry,
  CronJob,
  CustomResource,
  CustomResourceDefinition,
  HelmRelease,
  Ingress,
  Job,
  Namespace,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  Route,
  Service,
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
 * One band of the composition bar. `slot` indexes the deck's eight chart
 * tokens, which are a validated colour-blindness set in that order — so a slice
 * carries its slot rather than a colour, and nothing here may reorder them or
 * invent a ninth.
 */
export interface InsightSlice {
  key: string
  label: string
  value: number
  /** Share of the whole, 0–1, for the bar's own width. */
  share: number
  slot: number
}

/**
 * How a list is composed, along whichever axis is worth knowing for that kind:
 * namespaces for most, roles for Nodes, API groups for CRDs, storage classes for
 * PVs. It is deliberately not a second state reading — it answers "where is all
 * this" rather than "is it working".
 */
export interface InsightDistribution {
  /** What the axis is, as the band's own micro-caps heading. */
  label: string
  slices: InsightSlice[]
  /** How many distinct values were folded into the final slice. */
  folded: number
}

export interface ResourceInsight {
  /** What the whole list is, in one sentence, for the header's own line. */
  headline: string
  /** The dot beside that sentence. `idle` is what an inventory reads as. */
  headlineTone: Tone
  /**
   * The one or two readings drawn large. The first is always the total, so it is
   * also what the collapsed line leads with.
   */
  lead: InsightStat[]
  /** The state or composition list beside them, empty buckets left out. */
  breakdown: InsightStat[]
  /** How this list is spread, or null when there is nothing to spread it over. */
  distribution: InsightDistribution | null
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
 * How many bands the composition bar draws. It is the chart palette's length
 * because that palette *is* the colour-blindness mechanism: past eight, the ninth
 * value would either repeat a hue or rest identity on position alone, so the
 * tail folds into one band that says how many it stands for.
 */
export const MAX_SLICES = 8

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
  for (const stat of [...insight.lead, ...insight.breakdown]) {
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
 * The composition band. It is drawn only where there is genuinely a spread: one
 * value covering everything is a bar with a single band and a list with a single
 * row, which restates the total and earns none of the width it takes.
 */
function distribute<T>(
  label: string,
  rows: T[],
  value: (row: T) => string | undefined | null,
): InsightDistribution | null {
  const entries = ranked(tally(rows, value))
  if (entries.length < 2) return null

  const total = entries.reduce((sum, [, count]) => sum + count, 0)
  const named = entries.length > MAX_SLICES ? entries.slice(0, MAX_SLICES - 1) : entries
  const slices: InsightSlice[] = named.map(([key, count], index) => ({
    key,
    label: key,
    value: count,
    share: count / total,
    slot: index,
  }))

  const rest = entries.slice(named.length)
  if (rest.length > 0) {
    const count = rest.reduce((sum, [, n]) => sum + n, 0)
    slices.push({
      key: ' other',
      label: `${rest.length} more`,
      value: count,
      share: count / total,
      slot: MAX_SLICES - 1,
    })
  }

  return { label, slices, folded: rest.length }
}

/**
 * The state list for a kind whose states are an open set the cluster names
 * rather than a closed one this file can enumerate — a Service type, a PV phase,
 * a Helm status. Ordered by count so the dominant state leads, and never
 * selectable: there is no matcher behind these, and a control that does nothing
 * is worse than a number.
 */
function states<T>(rows: T[], value: (row: T) => string, tone: (state: string) => Tone) {
  return ranked(tally(rows, value)).map(([state, count]) => ({
    id: 'all' as const,
    label: state,
    value: count,
    tone: tone(state),
    selectable: false,
  }))
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
  breakdown: InsightStat[],
  distribution: InsightDistribution | null,
  headline: string,
  summary: string[],
): ResourceInsight {
  return {
    headline,
    headlineTone: 'idle',
    lead: [reading(label, count)],
    breakdown,
    distribution,
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
    lead: [reading(label, 0)],
    breakdown: [],
    distribution: null,
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

  const breakdown: InsightStat[] = []
  const optional: Array<[Exclude<PodBucket, 'all' | 'restarting'>, string, Tone]> = [
    ['notready', 'Not ready', 'warn'],
    ['pending', 'Pending', 'warn'],
    ['failed', 'Failed', 'bad'],
    ['unknown', 'Unknown', 'warn'],
    ['succeeded', 'Succeeded', 'idle'],
  ]
  for (const [id, name, tone] of optional) {
    if (counts[id] > 0) breakdown.push({ id, label: name, value: counts[id], tone, selectable: true })
  }

  if (restarts > 0) {
    // The value counts restarts and the detail counts pods, because both
    // answer something: one restart across forty pods is a rollout, forty
    // restarts on one pod is a crash loop.
    breakdown.push({
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
  const summary: string[] = []
  if (aggregate) summary.push(`CPU ${formatCPU(aggregate.cpu)}`, `MEM ${formatMemory(aggregate.memory)}`)
  if (restarts > 0) summary.push(plural(restarts, 'restart'))

  return {
    headline:
      unhealthy === 0
        ? `All ${plural(pods.length, 'pod')} are running`
        : `${plural(unhealthy, 'pod')} not running normally`,
    headlineTone: unhealthy === 0 ? (restarts > 0 ? 'warn' : 'ok') : counts.failed > 0 ? 'bad' : 'warn',
    lead: [
      { id: 'all', label, value: pods.length, selectable: true },
      { id: 'running', label: 'Running', value: counts.running, tone: 'ok', selectable: true },
    ],
    breakdown,
    distribution: distribute('Namespaces', pods, (pod) => pod.namespace),
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

  const breakdown: InsightStat[] = []
  const optional: Array<[Exclude<WorkloadBucket, 'all' | 'available'>, string, Tone]> = [
    ['degraded', 'Degraded', 'warn'],
    ['unavailable', 'Unavailable', 'bad'],
    ['scaledtozero', 'Scaled to zero', 'idle'],
  ]
  for (const [id, name, tone] of optional) {
    if (counts[id] > 0) breakdown.push({ id, label: name, value: counts[id], tone, selectable: true })
  }

  if (desired > 0) {
    breakdown.push({
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
      failing === 0
        ? 'Every workload has the replicas it asked for'
        : `${failing} of ${workloads.length} below their desired replicas`,
    headlineTone: failing === 0 ? 'ok' : counts.unavailable > 0 ? 'bad' : 'warn',
    lead: [
      { id: 'all', label, value: workloads.length, selectable: true },
      { id: 'available', label: 'Available', value: counts.available, tone: 'ok', selectable: true },
    ],
    breakdown,
    distribution: distribute('Namespaces', workloads, (workload) => workload.namespace),
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
    lead: [reading(label, jobs.length), reading('Complete', complete, undefined, 'ok')],
    breakdown: [
      ...states(jobs, (job) => job.state || 'Unknown', jobStateTone),
      ...(running > 0 ? [reading('Active pods', running, undefined, 'accent')] : []),
    ],
    distribution: distribute('Namespaces', jobs, (job) => job.namespace),
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
    lead: [reading(label, cronjobs.length), reading('Active', active, undefined, 'ok')],
    breakdown: [
      ...(suspended > 0 ? [reading('Suspended', suspended, undefined, 'idle')] : []),
      ...(running > 0 ? [reading('Running now', running, 'jobs in flight', 'accent')] : []),
    ],
    distribution: distribute('Namespaces', cronjobs, (job) => job.namespace),
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
    distribute('Namespaces', services, (service) => service.namespace),
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
    lead: [
      reading(label, ingresses.length),
      reading('Addressed', ingresses.length - pending.length, undefined, 'ok'),
    ],
    breakdown: [
      ...(pending.length > 0 ? [reading('No address', pending.length, undefined, 'warn')] : []),
      reading('Hosts', hosts),
      reading(
        'Rules',
        ingresses.reduce((sum, ingress) => sum + ingress.rules, 0),
      ),
    ],
    distribution: distribute('Ingress classes', ingresses, (ingress) => ingress.class || 'default'),
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [plural(hosts, 'host')],
  }
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
    lead: [
      reading(label, routes.length),
      reading('Attached', routes.length - orphaned.length, undefined, 'ok'),
    ],
    breakdown: [
      ...(orphaned.length > 0 ? [reading('Unattached', orphaned.length, undefined, 'warn')] : []),
      reading('Hostnames', hosts),
      reading(
        'Rules',
        routes.reduce((sum, route) => sum + route.rules, 0),
      ),
    ],
    distribution: distribute('Namespaces', routes, (route) => route.namespace),
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
    lead: [reading(label, releases.length), reading('Deployed', deployed, undefined, 'ok')],
    breakdown: [
      ...states(releases, (release) => release.status || 'unknown', helmStatusTone),
      reading('Charts', new Set(releases.map((release) => release.chart_name)).size),
    ],
    distribution: distribute('Namespaces', releases, (release) => release.namespace),
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
    lead: [reading(label, volumes.length), reading('Bound', bound, undefined, 'ok')],
    breakdown: states(volumes, (volume) => volume.status || 'Unknown', volumeStatusTone),
    distribution: distribute('Storage classes', volumes, (volume) => volume.storage_class || 'none'),
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
    lead: [reading(label, claims.length), reading('Bound', bound, undefined, 'ok')],
    breakdown: states(claims, (claim) => claim.status || 'Unknown', volumeStatusTone),
    distribution: distribute('Storage classes', claims, (claim) => claim.storage_class || 'none'),
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${bound}/${claims.length} bound`],
  }
}

/** Which provisioner backs the cluster's storage, and whether one is the default. */
export function storageClassInsights(classes: StorageClass[], label: string): ResourceInsight {
  if (classes.length === 0) return nothing(label, 'No storage classes here')

  const defaults = classes.filter((entry) => entry.default).length

  return inventory(
    label,
    classes.length,
    [
      // No default class means a claim with no class named stays Pending
      // forever, which is worth the eye landing on.
      reading('Default', defaults, undefined, defaults === 1 ? 'ok' : 'warn'),
      ...states(classes, (entry) => entry.binding_mode || 'Unknown', () => 'idle'),
    ],
    distribute('Provisioners', classes, (entry) => entry.provisioner),
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
    [
      reading('Keys', keys),
      ...(immutable > 0 ? [reading('Immutable', immutable, undefined, 'idle')] : []),
    ],
    secrets
      ? distribute('Types', entries, (entry) => entry.type || 'Opaque')
      : distribute('Namespaces', entries, (entry) => entry.namespace),
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
    [
      reading('Namespaced', namespaced),
      reading('Cluster-scoped', crds.length - namespaced),
      reading('API groups', groups),
    ],
    distribute('API groups', crds, (crd) => crd.group),
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
    [reading('Kinds', new Set(rows.map((row) => row.kind).filter(Boolean)).size)],
    distribute('Namespaces', rows, (row) => row.namespace),
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
    lead: [reading(label, nodes.length), reading('Ready', ready, undefined, 'ok')],
    breakdown: [
      ...(down.length > 0 ? [reading('Not ready', down.length, undefined, 'bad')] : []),
      ...(cordoned > 0 ? [reading('Cordoned', cordoned, 'unschedulable', 'warn')] : []),
      reading('Kubelet versions', new Set(nodes.map((node) => node.version)).size),
    ],
    // Roles rather than namespaces: a node has no namespace, and how the
    // control plane and the workers split is the shape of the cluster.
    distribution: distribute('Roles', nodes, (node) => node.roles[0] ?? 'none'),
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${ready}/${nodes.length} ready`],
  }
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
    lead: [reading(label, namespaces.length), reading('Active', active, undefined, 'ok')],
    breakdown: [
      ...(terminating.length > 0
        ? [reading('Terminating', terminating.length, undefined, 'warn')]
        : []),
      // What the grant actually covers, which is the question a namespace list
      // is usually opened to answer.
      reading('Granted to you', granted, granted === namespaces.length ? 'all of them' : undefined),
    ],
    distribution: distribute('Status', namespaces, (entry) => entry.status || 'Unknown'),
    alerts: alerts.slice(0, MAX_ALERTS),
    alerting: alerts.length,
    summary: [`${granted} granted`],
  }
}
