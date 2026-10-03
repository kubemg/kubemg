import type { CompareRow, Pod } from '../api/types'
import type { PodUsageIndex } from './units'

/*
 * What one namespace is spending, and on what, and which of its pods are in
 * trouble for a reason the phase bar cannot show.
 *
 * The pilot header's bar is a partition by *phase*, and that is the right shape
 * for "is it all right". It is the wrong shape for four questions an operator
 * opens a namespace with, because none of them is a phase: a pod that restarts
 * every ten minutes, a pod OOM-killed an hour ago and a pod throttled to half
 * its CPU are all `Running`. So these are read here, beside the partition
 * rather than inside it, each one a count and the pods behind it — the header
 * names the worst and the table holds the rest.
 *
 * The composition is per **workload**, not per pod. Eight replicas of one
 * Deployment are one thing spending CPU, and a donut of eight equal slices
 * would hide the only fact worth drawing: which workload the namespace is
 * paying for. The owner arrives on the pod row (the server resolves a
 * Deployment's ReplicaSet up to the Deployment), so this costs no read.
 *
 * Pure: no React, no fetch. Every number is from rows already loaded, except
 * throttling — the only one that needs history — which is handed the server's
 * own ranked answer rather than reading anything itself.
 */

/** Which reading the composition divides. */
export type ConsumptionAxis = 'cpu' | 'memory'

/**
 * How many slices the composition draws before the rest become one. Eight is
 * the deck's categorical palette and a ninth colour does not exist (index.css):
 * seven named workloads and one "N more", which takes the eighth slot exactly as
 * the bar's composition does.
 */
export const MAX_SLICES = 8

/** The share of a container's own limit at which its pod counts as "at its limit". */
export const LIMIT_PRESSURE = 0.9

/**
 * The throttled share of CFS periods that counts as throttled. 25% is the
 * threshold kube-prometheus' own `CPUThrottlingHigh` alert fires at, so this
 * header and the alert an operator already receives agree about what "being
 * throttled" means.
 */
export const THROTTLE_PRESSURE = 0.25

/** How many pods a column names before it counts instead. */
export const NAMED_SIGNALS = 3

/** Container states that mean the image never arrived. The cluster's own words. */
const IMAGE_FAILURES = new Set(['ImagePullBackOff', 'ErrImagePull', 'InvalidImageName'])

/** The workload a pod belongs to, or the pod itself when nothing owns it. */
export interface WorkloadRef {
  kind: string
  name: string
}

export function workloadOf(pod: Pod): WorkloadRef {
  return pod.owner ? { kind: pod.owner.kind, name: pod.owner.name } : { kind: 'Pod', name: pod.name }
}

export interface ConsumptionSlice {
  key: string
  /** The workload's name, or "N more" for the folded tail. */
  label: string
  /** The workload's kind; empty on the folded tail, which is several kinds. */
  kind: string
  value: number
  /** Share of the namespace's total, 0–1. */
  share: number
  /** Index into the deck's `chart-1..8` slots. */
  slot: number
  /** How many pods this slice sums. */
  pods: number
}

export interface Consumption {
  axis: ConsumptionAxis
  total: number
  slices: ConsumptionSlice[]
  /** How many workloads spend anything, which is more than the slices name. */
  workloads: number
  /** Pods with a live sample, against the pods in the namespace. */
  sampled: number
  pods: number
}

/**
 * Divides the namespace's live usage per workload. Null where there is no live
 * sample at all — metrics-server is optional, and "not measured" must not be
 * drawn as "spends nothing".
 */
export function consumption(
  pods: Pod[],
  usage: PodUsageIndex | null,
  axis: ConsumptionAxis,
): Consumption | null {
  if (!usage) return null

  const sums = new Map<string, { ref: WorkloadRef; value: number; pods: number }>()
  let sampled = 0
  for (const pod of pods) {
    const sample = usage.get(`${pod.namespace}/${pod.name}`)
    if (!sample) continue
    sampled += 1
    const value = axis === 'cpu' ? sample.cpu_millicores : sample.memory_bytes
    const ref = workloadOf(pod)
    const key = `${ref.kind}/${ref.name}`
    const entry = sums.get(key) ?? { ref, value: 0, pods: 0 }
    entry.value += value
    entry.pods += 1
    sums.set(key, entry)
  }
  if (sampled === 0) return null

  const ranked = [...sums]
    .filter(([, entry]) => entry.value > 0)
    .sort((a, b) => b[1].value - a[1].value || a[0].localeCompare(b[0]))
  const total = ranked.reduce((sum, [, entry]) => sum + entry.value, 0)

  const named = ranked.length > MAX_SLICES ? ranked.slice(0, MAX_SLICES - 1) : ranked
  const slices: ConsumptionSlice[] = named.map(([key, entry], index) => ({
    key,
    label: entry.ref.name,
    kind: entry.ref.kind,
    value: entry.value,
    share: total > 0 ? entry.value / total : 0,
    slot: index,
    pods: entry.pods,
  }))

  const rest = ranked.slice(named.length)
  if (rest.length > 0) {
    const value = rest.reduce((sum, [, entry]) => sum + entry.value, 0)
    slices.push({
      key: 'rest',
      label: `${rest.length} more`,
      kind: '',
      value,
      share: total > 0 ? value / total : 0,
      slot: MAX_SLICES - 1,
      pods: rest.reduce((sum, [, entry]) => sum + entry.pods, 0),
    })
  }

  return { axis, total, slices, workloads: ranked.length, sampled, pods: pods.length }
}

/** One pod a column names, and why. */
export interface SignalPod {
  key: string
  name: string
  namespace: string
  detail: string
  tone: 'warn' | 'bad'
}

export interface Signal {
  /** How many pods are in this state — what the column leads with. */
  count: number
  /** Every one of them, worst first. The column draws the first few. */
  pods: SignalPod[]
}

function signal(entries: Array<SignalPod & { weight: number }>): Signal {
  const pods = entries
    .sort((a, b) => b.weight - a.weight || a.name.localeCompare(b.name))
    .map(({ weight: _weight, ...pod }) => pod)
  return { count: pods.length, pods }
}

function keyOf(pod: Pod): string {
  return `${pod.namespace}/${pod.name}`
}

/**
 * Pods that have restarted, most first. `restarts` is the namespace's total,
 * because both numbers answer something: one restart across forty pods is a
 * rollout, forty on one pod is a crash loop.
 */
export function restartSignal(pods: Pod[]): Signal & { restarts: number } {
  let restarts = 0
  const entries: Array<SignalPod & { weight: number }> = []
  for (const pod of pods) {
    if (pod.restarts <= 0) continue
    restarts += pod.restarts
    const looping = pod.containers.some((container) => container.state === 'CrashLoopBackOff')
    entries.push({
      key: keyOf(pod),
      name: pod.name,
      namespace: pod.namespace,
      detail: looping ? `CrashLoopBackOff · ${pod.restarts}` : `${pod.restarts}`,
      tone: looping ? 'bad' : 'warn',
      weight: pod.restarts,
    })
  }
  return { ...signal(entries), restarts }
}

/** Pods with a container whose image never arrived, in the kubelet's own words. */
export function imagePullSignal(pods: Pod[]): Signal {
  const entries: Array<SignalPod & { weight: number }> = []
  for (const pod of pods) {
    const failing = pod.containers.find((container) => IMAGE_FAILURES.has(container.state))
    if (!failing) continue
    entries.push({
      key: keyOf(pod),
      name: pod.name,
      namespace: pod.namespace,
      detail: failing.state,
      tone: 'bad',
      weight: 0,
    })
  }
  return signal(entries)
}

/**
 * Pods at or near a declared limit.
 *
 * Two kinds of evidence, and the stronger leads. A container whose previous run
 * ended `OOMKilled` *reached* its memory limit — the kernel is what enforces it
 * — and that holds even though it is Running again now. Short of that, a live
 * sample at `LIMIT_PRESSURE` of a container's own limit is a pod about to be
 * killed (memory) or already capped (CPU). The comparison is per container
 * against its own limit, because a pod at its limit is usually one container at
 * its limit, and a pod-level sum would average that away.
 *
 * `limited` counts the pods that declare any limit at all: a namespace where
 * nothing is limited cannot have anything at a limit, and saying so is more use
 * than a zero.
 */
export function limitSignal(
  pods: Pod[],
  usage: PodUsageIndex | null,
): Signal & { limited: number; measured: boolean } {
  let limited = 0
  const entries: Array<SignalPod & { weight: number }> = []

  for (const pod of pods) {
    const declares = pod.containers.some(
      (container) => container.cpu_limit_millicores > 0 || container.memory_limit_bytes > 0,
    )
    if (declares) limited += 1

    const base = { key: keyOf(pod), name: pod.name, namespace: pod.namespace }
    const killed = pod.containers.find(
      (container) =>
        container.state === 'OOMKilled' || container.last_termination_reason === 'OOMKilled',
    )
    if (killed) {
      entries.push({ ...base, detail: 'OOMKilled', tone: 'bad', weight: 2 })
      continue
    }

    const sample = usage?.get(base.key)
    if (!sample || !declares) continue

    let worst: { ratio: number; resource: 'CPU' | 'memory' } | null = null
    for (const container of pod.containers) {
      const live = sample.containers.find((entry) => entry.name === container.name)
      if (!live) continue
      const readings: Array<[number, number, 'CPU' | 'memory']> = [
        [live.memory_bytes, container.memory_limit_bytes, 'memory'],
        [live.cpu_millicores, container.cpu_limit_millicores, 'CPU'],
      ]
      for (const [used, limit, resource] of readings) {
        if (limit <= 0) continue
        const ratio = used / limit
        if (!worst || ratio > worst.ratio) worst = { ratio, resource }
      }
    }
    if (worst && worst.ratio >= LIMIT_PRESSURE) {
      entries.push({
        ...base,
        detail: `${worst.resource} ${Math.round(worst.ratio * 100)}%`,
        // Memory past the line is a kill on its way; CPU past it is a cap the
        // pod is already living under. Both are worth a look, one is worse.
        tone: worst.resource === 'memory' ? 'bad' : 'warn',
        weight: worst.ratio,
      })
    }
  }

  return { ...signal(entries), limited, measured: usage !== null }
}

/**
 * Pods throttled past `THROTTLE_PRESSURE` over the window, from the server's
 * ranked answer for the `cpu_throttling` reading — the ratio of CFS periods in
 * which a container was throttled. Rows are pods (the reading's legend), and
 * the ranking is already worst first.
 */
export function throttleSignal(rows: CompareRow[], namespace: string): Signal {
  return signal(
    rows
      .filter((row) => Number.isFinite(row.current) && row.current >= THROTTLE_PRESSURE)
      .map((row) => ({
        key: `${namespace}/${row.name}`,
        name: row.name,
        namespace,
        detail: `${Math.round(row.current * 100)}%`,
        tone: row.current >= 0.5 ? ('bad' as const) : ('warn' as const),
        weight: row.current,
      })),
  )
}
