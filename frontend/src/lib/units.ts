/*
 * Rendering for the two quantities the metrics endpoints return. The backend
 * normalises everything to millicores and bytes so the browser never parses a
 * Kubernetes quantity; all that is left here is choosing a unit a person reads
 * at a glance.
 */

import type { PodContainer, PodUsage } from '../api/types'

/** formatCPU renders millicores the way `kubectl top` does: milli, then cores. */
export function formatCPU(millicores: number): string {
  if (!Number.isFinite(millicores) || millicores <= 0) return '0m'
  if (millicores < 1000) return `${Math.round(millicores)}m`

  const cores = millicores / 1000
  return `${cores < 10 ? cores.toFixed(2) : cores.toFixed(1)} cores`
}

const MEMORY_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

/**
 * formatMemory renders bytes in binary units, because that is what Kubernetes
 * reports and what a limit is written in — showing "1.07 GB" for a 1Gi limit
 * makes the two look like different numbers.
 */
export function formatMemory(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0'

  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < MEMORY_UNITS.length - 1) {
    value /= 1024
    unit += 1
  }
  const digits = value < 10 && unit > 0 ? 1 : 0
  return `${value.toFixed(digits)} ${MEMORY_UNITS[unit]}`
}

/** ratio is a used/total percentage clamped to something a bar can draw. */
export function ratio(used: number, total: number): number {
  if (!Number.isFinite(used) || !Number.isFinite(total) || total <= 0) return 0
  return Math.min(100, Math.max(0, (used / total) * 100))
}

/**
 * usageTone maps utilisation onto the deck's semantic states. The thresholds
 * are the ones an operator already carries in their head: comfortable below
 * 75%, worth a look by 90%.
 */
export function usageTone(percent: number): 'ok' | 'warn' | 'bad' {
  if (percent >= 90) return 'bad'
  if (percent >= 75) return 'warn'
  return 'ok'
}

/**
 * podLimit sums a pod's container limits, and answers 0 — "unbounded" — unless
 * *every* container declares one: a single unlimited container makes the pod
 * unlimited, so a total across the rest would be a ceiling that does not exist.
 * It lives here rather than beside either of its callers because the pod list
 * and the pod drawer must not disagree about what a pod's ceiling is.
 */
export function podLimit(containers: PodContainer[], resource: 'cpu' | 'memory'): number {
  let total = 0
  for (const container of containers) {
    const limit =
      resource === 'cpu' ? container.cpu_limit_millicores : container.memory_limit_bytes
    if (limit <= 0) return 0
    total += limit
  }
  return total
}

/**
 * Live usage keyed by `namespace/name`, which is what identifies a pod in a list
 * that may span namespaces. Built once per load rather than searched per row: a
 * scan per row over a thousand-pod cluster is a thousand scans.
 */
export type PodUsageIndex = Map<string, PodUsage>

/** podUsageIndex keys a metrics list the way a pod row can look itself up. */
export function podUsageIndex(pods: PodUsage[]): PodUsageIndex {
  return new Map(pods.map((usage) => [`${usage.namespace}/${usage.name}`, usage]))
}
