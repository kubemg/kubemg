import type { CapacitySeverity, NodeCapacity } from '../api/types'

/*
 * The capacity heatmap: every node as one row, every ceiling as one cell, so a
 * fleet of two hundred nodes is one screen rather than two hundred bars.
 *
 * Four rules, each pinned in the test beside this file:
 *
 *   - **A cell's colour mirrors the server's thresholds and never adds one.**
 *     Amber and rust land exactly where the server writes a `warn` or `danger`
 *     concern for that column; below them the cell only gets denser. A column
 *     the server raises no warning for (CPU in use — a CPU shortage honours
 *     every request) never turns amber, however full.
 *   - **Unknown is not zero.** A node reporting no allocatable, or a usage
 *     column with no Metrics API behind it, reads `null` and draws a dash —
 *     an empty cell that looked like 0% would say "idle" while knowing nothing.
 *   - **Limits are written, not drawn.** A limit can be 340% of a node, and the
 *     cell says 340%; the shade is the verdict and the number is the reading.
 *   - **Sorting puts what cannot answer last**, in either direction, so a sort
 *     by "in use" never leads with the nodes it knows nothing about.
 */

export type HeatKey =
  | 'cpu_requested'
  | 'cpu_used'
  | 'cpu_limited'
  | 'memory_requested'
  | 'memory_used'
  | 'memory_limited'
  | 'pods'

export type HeatLevel = 'unknown' | 'plain' | 'heat-1' | 'heat-2' | 'heat-3' | 'warn' | 'danger'

export interface HeatColumn {
  key: HeatKey
  resource: 'CPU' | 'Memory' | 'Pods'
  label: string
  /** Where the server writes a `warn` concern for this column; null where it writes none. */
  warnAt: number | null
  /** Where the server writes a `danger` concern; null where it writes none. */
  dangerAt: number | null
  /** Whether this column needs the Metrics API to answer at all. */
  usage: boolean
}

/* These mirror the server's own thresholds — 90% committed, 100% exhausted,
   90% memory in use, 200% CPU limits, 100% memory limits — and a change to one
   side is a change to both: a cell that turned amber where no concern is
   written would be a claim with nothing behind it. */
export const HEAT_COLUMNS: readonly HeatColumn[] = [
  { key: 'cpu_requested', resource: 'CPU', label: 'Reserved', warnAt: 90, dangerAt: 100, usage: false },
  { key: 'cpu_used', resource: 'CPU', label: 'In use', warnAt: null, dangerAt: null, usage: true },
  { key: 'cpu_limited', resource: 'CPU', label: 'Limits', warnAt: 200, dangerAt: null, usage: false },
  { key: 'memory_requested', resource: 'Memory', label: 'Reserved', warnAt: 90, dangerAt: 100, usage: false },
  { key: 'memory_used', resource: 'Memory', label: 'In use', warnAt: 90, dangerAt: null, usage: true },
  { key: 'memory_limited', resource: 'Memory', label: 'Limits', warnAt: 100, dangerAt: null, usage: false },
  { key: 'pods', resource: 'Pods', label: 'Slots', warnAt: 90, dangerAt: 100, usage: false },
]

/** heatValue reads one cell: a percentage of allocatable, or null when the node cannot answer. */
export function heatValue(node: NodeCapacity, key: HeatKey, usageAvailable: boolean): number | null {
  switch (key) {
    case 'cpu_requested':
      return node.cpu.allocatable > 0 ? node.cpu.requested_percent : null
    case 'cpu_limited':
      return node.cpu.allocatable > 0 ? node.cpu.limited_percent : null
    case 'cpu_used':
      // A node the Metrics API left out reports 0, and no running node uses
      // exactly nothing — so 0 here is "not measured", as the bars read it.
      return usageAvailable && node.cpu.allocatable > 0 && node.cpu.used > 0
        ? node.cpu.used_percent
        : null
    case 'memory_requested':
      return node.memory.allocatable > 0 ? node.memory.requested_percent : null
    case 'memory_limited':
      return node.memory.allocatable > 0 ? node.memory.limited_percent : null
    case 'memory_used':
      return usageAvailable && node.memory.allocatable > 0 && node.memory.used > 0
        ? node.memory.used_percent
        : null
    case 'pods':
      return node.pods.allocatable > 0 ? node.pods.percent : null
  }
}

/**
 * heatLevel shades one cell. Past a threshold the server reasons with, the
 * cell takes that threshold's tone; below it, density in quarters of the way
 * to the first threshold (or to 100% for a column that has none).
 */
export function heatLevel(value: number | null, column: HeatColumn): HeatLevel {
  if (value === null) return 'unknown'
  if (column.dangerAt !== null && value >= column.dangerAt) return 'danger'
  if (column.warnAt !== null && value >= column.warnAt) return 'warn'
  const scale = column.warnAt ?? 100
  const share = value / scale
  if (share < 0.25) return 'plain'
  if (share < 0.5) return 'heat-1'
  if (share < 0.75) return 'heat-2'
  return 'heat-3'
}

export type HeatSort = 'name' | 'severity' | HeatKey

const SEVERITY_RANK: Record<CapacitySeverity, number> = { ok: 0, note: 1, warn: 2, danger: 3 }

/**
 * sortNodes orders the rows. Name ascends; everything else descends, fullest
 * or worst first, because that is the question a sort by it asks. Ties fall
 * back to the name so a live refresh never reshuffles equal rows.
 */
export function sortNodes(
  nodes: readonly NodeCapacity[],
  by: HeatSort,
  usageAvailable: boolean,
): NodeCapacity[] {
  const byName = (a: NodeCapacity, b: NodeCapacity) => a.name.localeCompare(b.name)
  const out = [...nodes]
  if (by === 'name') return out.sort(byName)
  if (by === 'severity') {
    return out.sort((a, b) => SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity] || byName(a, b))
  }
  return out.sort((a, b) => {
    const left = heatValue(a, by, usageAvailable)
    const right = heatValue(b, by, usageAvailable)
    if (left === null && right === null) return byName(a, b)
    if (left === null) return 1
    if (right === null) return -1
    return right - left || byName(a, b)
  })
}

export interface NodeGroup {
  /** The role the group is named for, as the node labels spell it. */
  role: string
  nodes: NodeCapacity[]
}

const CONTROL_PLANE_ROLES = ['control-plane', 'master']

/**
 * groupNodes splits the rows by role, control plane first, keeping the order
 * they arrived in within each group. A control-plane node is often tainted and
 * small, and read beside the workers it makes the fleet look fuller or emptier
 * than the part that takes work is.
 */
export function groupNodes(nodes: readonly NodeCapacity[]): NodeGroup[] {
  const groups = new Map<string, NodeCapacity[]>()
  for (const node of nodes) {
    const role = node.roles.some((entry) => CONTROL_PLANE_ROLES.includes(entry))
      ? 'control-plane'
      : node.roles.join(', ') || 'worker'
    const group = groups.get(role)
    if (group) group.push(node)
    else groups.set(role, [node])
  }
  return [...groups.entries()]
    .map(([role, members]) => ({ role, nodes: members }))
    .sort((a, b) => {
      if (a.role === 'control-plane') return -1
      if (b.role === 'control-plane') return 1
      return a.role.localeCompare(b.role)
    })
}

/** qosLine writes a node's QoS split in a fixed order, leaving out the empty classes. */
export function qosLine(qos: NodeCapacity['qos']): string {
  const parts: string[] = []
  if (qos.guaranteed > 0) parts.push(`${qos.guaranteed} Guaranteed`)
  if (qos.burstable > 0) parts.push(`${qos.burstable} Burstable`)
  if (qos.best_effort > 0) parts.push(`${qos.best_effort} BestEffort`)
  return parts.join(' · ')
}
