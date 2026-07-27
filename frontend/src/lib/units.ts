/*
 * Rendering for the two quantities the metrics endpoints return. The backend
 * normalises everything to millicores and bytes so the browser never parses a
 * Kubernetes quantity; all that is left here is choosing a unit a person reads
 * at a glance.
 */

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
