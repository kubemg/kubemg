/**
 * The waste report's shape, and the one place a partial answer is completed.
 *
 * Every number a reading could not produce is **absent**, not zero: a request
 * the container never declared, a usage the datasource never recorded, a
 * recommendation for a workload too young to size. The page draws absent as a
 * dash with the reason beside it, and a default filled in here would turn that
 * into "measured, and nothing" — which is exactly the wrong claim on a page
 * whose subject is how much is unused. Money follows the same rule: a figure
 * is present only where the cluster has a rate for it.
 */

export type WasteSpan = '1d' | '7d' | '14d' | '30d'

export const WASTE_SPANS: readonly WasteSpan[] = ['1d', '7d', '14d', '30d']

export interface WasteDimension {
  /** Millicores for CPU, bytes for memory. */
  request?: number
  observed?: number
  recommended?: number
  /** The dimension the recommendation lowers. */
  over: boolean
}

export interface WasteContainer {
  name: string
  sidecar?: boolean
  cpu: WasteDimension
  memory: WasteDimension
  note?: string
}

export interface WasteAmount {
  cpu: number
  memory: number
}

export interface WasteWorkload {
  kind: 'Deployment' | 'StatefulSet' | 'DaemonSet'
  namespace: string
  name: string
  replicas: number
  /** Where the reading starts: the later of the oldest pod's start and the first sample. */
  since: string
  rollout?: boolean
  containers: WasteContainer[]
  reclaimable: WasteAmount
  /** What the reclaimable capacity costs a month at the cluster's rates. */
  monthly?: number
}

export type WasteVolumeState = 'unmounted' | 'released' | 'available'

export interface WasteVolume {
  kind: 'PersistentVolumeClaim' | 'PersistentVolume'
  namespace?: string
  name: string
  capacity: number
  storage_class?: string
  state: WasteVolumeState
  volume?: string
  claim?: string
  reclaim_policy?: string
  statefulset?: string
  scaled_down?: boolean
  created_at: string
  monthly?: number
}

export interface WasteLoadBalancer {
  namespace: string
  name: string
  addresses: string[]
  state: 'no-ready-endpoints' | 'no-selector'
  created_at: string
  monthly?: number
}

export interface WasteSummary {
  reclaimable_cpu: number
  reclaimable_memory: number
  unused_storage: number
  idle_load_balancers: number
  workloads_checked: number
  workloads_short_history: number
  workloads_unmeasured: number
  /** Present only where the cluster has rates. */
  monthly?: WasteMonthly
}

export type UnpricedCategory = 'storage' | 'load_balancers'

export interface WasteMonthly {
  reclaimable: number
  unused_storage?: number
  idle_load_balancers?: number
  /** What every priced category adds up to. */
  total: number
  /** Categories with something in them and no rate, which the total leaves out. */
  unpriced: UnpricedCategory[]
}

/** A cluster's unit prices, as an admin entered them. */
export interface CostRates {
  currency: string
  cpu_core_hour: number
  memory_gib_hour: number
  storage_gib_month?: number
  load_balancer_hour?: number
  updated_at?: string
}

export interface ReservedWorkload {
  kind: string
  name: string
  pods: number
  /** Millicores and bytes, as the scheduler counts them. */
  cpu: number
  memory: number
  monthly?: number
}

export interface ReservedNamespace {
  namespace: string
  pods: number
  cpu: number
  memory: number
  monthly?: number
  workloads: ReservedWorkload[]
  /** Exact, where `workloads` is capped. */
  workloads_total: number
}

/** What the placed pods reserve. Answered with or without rates or history. */
export interface Reserved {
  pods: number
  cpu: number
  memory: number
  monthly?: number
  namespaces: ReservedNamespace[]
  namespaces_total: number
  /** Pods waiting for a node, which reserve nothing yet. */
  unscheduled: number
}

export interface ClusterWaste {
  window: { span: WasteSpan; start: string; end: string }
  /** Whether usage history could be read; volumes and load balancers never depend on it. */
  workloads_available: boolean
  workloads_reason?: string
  workloads: WasteWorkload[]
  /** Exact, where `workloads` is capped. */
  workloads_total: number
  usage_truncated?: boolean
  volumes: WasteVolume[]
  /** False for a scoped grant, or where the cluster refused the list. */
  volumes_pv_available: boolean
  volumes_pv_reason?: string
  load_balancers: WasteLoadBalancer[]
  load_balancers_available: boolean
  load_balancers_reason?: string
  summary: WasteSummary
  reserved: Reserved
  /** Present only where the cluster has rates. */
  cost?: { currency: string; rates: CostRates; hours_per_month: number }
  /** Why nothing is priced, where that is not simply "no rates set". */
  cost_reason?: string
  /** The PromQL asked, so an empty answer can be diagnosed. */
  query?: { cpu: string; memory: string; history: string }
  truncated?: boolean
}

const EMPTY_SUMMARY: WasteSummary = {
  reclaimable_cpu: 0,
  reclaimable_memory: 0,
  unused_storage: 0,
  idle_load_balancers: 0,
  workloads_checked: 0,
  workloads_short_history: 0,
  workloads_unmeasured: 0,
}

/** completeWaste fills the lists and the summary, and nothing that is a reading. */
export function completeWaste(data: Partial<ClusterWaste>): ClusterWaste {
  return {
    window: data.window ?? { span: '7d', start: '', end: '' },
    workloads_available: data.workloads_available ?? false,
    workloads_reason: data.workloads_reason,
    workloads: (data.workloads ?? []).map((workload) => ({
      ...workload,
      containers: (workload.containers ?? []).map((container) => ({
        ...container,
        cpu: { ...container.cpu, over: container.cpu?.over ?? false },
        memory: { ...container.memory, over: container.memory?.over ?? false },
      })),
      reclaimable: workload.reclaimable ?? { cpu: 0, memory: 0 },
    })),
    workloads_total: data.workloads_total ?? data.workloads?.length ?? 0,
    usage_truncated: data.usage_truncated,
    volumes: data.volumes ?? [],
    volumes_pv_available: data.volumes_pv_available ?? false,
    volumes_pv_reason: data.volumes_pv_reason,
    load_balancers: (data.load_balancers ?? []).map((lb) => ({
      ...lb,
      addresses: lb.addresses ?? [],
    })),
    load_balancers_available: data.load_balancers_available ?? false,
    load_balancers_reason: data.load_balancers_reason,
    summary: {
      ...EMPTY_SUMMARY,
      ...data.summary,
      monthly: data.summary?.monthly
        ? { ...data.summary.monthly, unpriced: data.summary.monthly.unpriced ?? [] }
        : undefined,
    },
    reserved: {
      pods: data.reserved?.pods ?? 0,
      cpu: data.reserved?.cpu ?? 0,
      memory: data.reserved?.memory ?? 0,
      monthly: data.reserved?.monthly,
      namespaces: (data.reserved?.namespaces ?? []).map((namespace) => ({
        ...namespace,
        workloads: namespace.workloads ?? [],
        workloads_total: namespace.workloads_total ?? namespace.workloads?.length ?? 0,
      })),
      namespaces_total: data.reserved?.namespaces_total ?? data.reserved?.namespaces?.length ?? 0,
      unscheduled: data.reserved?.unscheduled ?? 0,
    },
    cost: data.cost,
    cost_reason: data.cost_reason,
    query: data.query,
    truncated: data.truncated,
  }
}
