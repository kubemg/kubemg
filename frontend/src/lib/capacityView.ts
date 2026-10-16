import type { NodeCapacity, QOSCounts } from '../api/types'

/*
 * The arithmetic behind the capacity page's pictures, kept out of the
 * components so the rules can be pinned:
 *
 *   - **Tones mirror the server's allocation lines** (90% committed, 100%
 *     exhausted) and add none of their own — the colour only says what a
 *     concern beside it already says.
 *   - **A segment that is zero takes no width**, and a bar of nothing draws
 *     empty rather than dividing by zero.
 *   - **The fit bar is the fragmentation reading drawn**: the most one node can
 *     take against what is free in total. Equal is a full bar; a sliver is
 *     capacity that exists only in sum.
 */

const PERCENT = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 })

/** A 0–100 reading as a whole percentage. */
export function percentText(percent: number): string {
  return PERCENT.format(percent / 100)
}

export type AllocationTone = 'ok' | 'warn' | 'bad'

export function allocationTone(percent: number): AllocationTone {
  if (percent >= 100) return 'bad'
  if (percent >= 90) return 'warn'
  return 'ok'
}

/** clampPercent bounds a reading to a drawable 0–100. */
export function clampPercent(percent: number): number {
  if (!Number.isFinite(percent)) return 0
  return Math.min(100, Math.max(0, percent))
}

export interface Segment {
  key: string
  label: string
  value: number
  /** A background utility from the deck's tokens. */
  fill: string
}

/** segmentShares turns counts into widths, dropping what is zero. */
export function segmentShares(segments: readonly Segment[]): (Segment & { share: number })[] {
  const total = segments.reduce((sum, segment) => sum + Math.max(0, segment.value), 0)
  if (total === 0) return []
  return segments
    .filter((segment) => segment.value > 0)
    .map((segment) => ({ ...segment, share: (segment.value / total) * 100 }))
}

/*
 * QoS classes are categories, not states, so they take the deck's categorical
 * slots in order — never sage/amber/rust, which would read as a verdict.
 */
export function qosSegments(qos: QOSCounts): Segment[] {
  return [
    { key: 'guaranteed', label: 'Guaranteed', value: qos.guaranteed, fill: 'bg-chart-1' },
    { key: 'burstable', label: 'Burstable', value: qos.burstable, fill: 'bg-chart-2' },
    { key: 'best_effort', label: 'BestEffort', value: qos.best_effort, fill: 'bg-chart-3' },
  ]
}

/**
 * nodeStateSegments splits the nodes by whether they take pods at all. It
 * reads the nodes rather than the summary's counts: a node that is both not
 * ready and cordoned is in both counts, and only the rows say which. Such a
 * node is counted once, as not ready, the worse of the two.
 */
export function nodeStateSegments(nodes: readonly NodeCapacity[]): Segment[] {
  const notReady = nodes.filter((node) => !node.ready).length
  const cordoned = nodes.filter((node) => node.ready && !node.schedulable).length
  return [
    { key: 'ready', label: 'Ready', value: nodes.length - notReady - cordoned, fill: 'bg-ok' },
    { key: 'cordoned', label: 'Cordoned', value: cordoned, fill: 'bg-warn' },
    { key: 'not_ready', label: 'Not ready', value: notReady, fill: 'bg-danger' },
  ]
}

/** fitShare is the largest single-node fit as a share of the free total, 0–100. */
export function fitShare(largest: number, free: number): number {
  if (free <= 0) return 0
  return clampPercent((largest / free) * 100)
}

/** The bar width for a ranked list: each row against the largest in it. */
export function relativeShare(value: number, values: readonly number[]): number {
  const max = Math.max(0, ...values)
  if (max <= 0) return 0
  return clampPercent((value / max) * 100)
}

/** Whether a node's row has anything behind its "details" toggle. */
export function nodeHasDetail(node: NodeCapacity): boolean {
  return (
    node.concerns.length > 0 ||
    node.top_requests.length > 0 ||
    node.top_borrowers.length > 0 ||
    node.taints.length > 0
  )
}
