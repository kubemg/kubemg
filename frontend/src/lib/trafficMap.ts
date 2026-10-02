import type { TrafficEdge, TrafficMap, TrafficNode, TrafficState } from '../api/types'

/*
 * Laying a traffic map out, and deciding what lights up when one hop is
 * pointed at.
 *
 * The server already says which column a node belongs in — entry, route,
 * Service, workload, pod — because that is a statement about the node's role
 * in the path, not a drawing decision. What is left is the order inside a
 * column and the curves between them, and both are small enough to do by hand:
 * a graph-layout library would be the largest dependency in the bundle to place
 * at most a few dozen boxes in five fixed columns.
 *
 * Pure: no React, no DOM, so every rule here is a test.
 */

export const NODE_WIDTH = 188
export const NODE_HEIGHT = 66
/** The gap between two columns no labelled edge crosses. */
export const COLUMN_GAP = 56
/** The gap a labelled edge crosses — host and path, weight, port — which is
    where the label is drawn, so it is sized for one. */
export const LABEL_GAP = 168
export const ROW_GAP = 14
export const PADDING = 16
/** Room above the first row for the column's title. */
export const HEADER = 30

export const COLUMN_TITLES = ['Entry', 'Route', 'Service', 'Workload', 'Pods']

export interface PlacedNode {
  node: TrafficNode
  x: number
  y: number
}

export interface PlacedEdge {
  edge: TrafficEdge
  /** An SVG path from the source's right edge to the target's left edge. */
  path: string
  labelX: number
  labelY: number
  /** How wide the label may be drawn: the gap it sits in, less a margin. */
  labelWidth: number
}

export interface TrafficLayout {
  width: number
  height: number
  columns: Array<{ title: string; x: number }>
  nodes: PlacedNode[]
  edges: PlacedEdge[]
}

/**
 * Places every node. Columns with nothing in them are dropped rather than drawn
 * as an empty gap — a Service no route sends to starts at the Service. Inside a
 * column, nodes sit in the order of the nodes that lead to them (the mean row
 * of their sources), so edges run as straight as five columns allow; the entry
 * column, which has no sources, takes the order of what it leads to.
 */
export function layoutTraffic(map: TrafficMap): TrafficLayout {
  const used = [...new Set(map.nodes.map((node) => node.column))].sort((a, b) => a - b)

  const byColumn = new Map<number, TrafficNode[]>()
  for (const node of map.nodes) {
    const list = byColumn.get(node.column) ?? []
    list.push(node)
    byColumn.set(node.column, list)
  }

  const row = new Map<string, number>()
  const order = (nodes: TrafficNode[], neighbours: (id: string) => string[]) => {
    const keyed = nodes.map((node, index) => {
      const rows = neighbours(node.id)
        .map((id) => row.get(id))
        .filter((value): value is number => value !== undefined)
      const key = rows.length > 0 ? rows.reduce((a, b) => a + b, 0) / rows.length : index
      return { node, key, index }
    })
    keyed.sort((a, b) => a.key - b.key || a.index - b.index)
    keyed.forEach((entry, index) => row.set(entry.node.id, index))
    return keyed.map((entry) => entry.node)
  }

  const sources = (id: string) => map.edges.filter((edge) => edge.to === id).map((edge) => edge.from)
  const targets = (id: string) => map.edges.filter((edge) => edge.from === id).map((edge) => edge.to)

  const ordered = new Map<number, TrafficNode[]>()
  // Insertion order seeds the rows, so a column with no sources still has one.
  for (const column of used) {
    const nodes = byColumn.get(column) ?? []
    nodes.forEach((node, index) => row.set(node.id, index))
  }
  for (const column of used.slice(1)) {
    ordered.set(column, order(byColumn.get(column) ?? [], sources))
  }
  if (used.length > 0) {
    ordered.set(used[0], order(byColumn.get(used[0]) ?? [], targets))
  }

  const tallest = Math.max(0, ...[...ordered.values()].map((nodes) => nodes.length))
  const columnHeight = (count: number) => count * NODE_HEIGHT + Math.max(0, count - 1) * ROW_GAP
  const height = PADDING * 2 + HEADER + columnHeight(tallest)

  // A gap is wide only where a labelled edge leaves the column before it.
  const columnOf = new Map(map.nodes.map((node) => [node.id, node.column]))
  const labelledFrom = new Set(
    map.edges.filter((edge) => edge.labels.length > 0).map((edge) => columnOf.get(edge.from)),
  )
  const left = new Map<number, number>()
  let cursor = PADDING
  used.forEach((column, index) => {
    left.set(column, cursor)
    cursor += NODE_WIDTH
    if (index < used.length - 1) cursor += labelledFrom.has(column) ? LABEL_GAP : COLUMN_GAP
  })
  const width = cursor + PADDING

  const placed = new Map<string, PlacedNode>()
  const nodes: PlacedNode[] = []
  for (const column of used) {
    const list = ordered.get(column) ?? []
    const x = left.get(column) ?? PADDING
    // A short column is centred against the tallest, which is what keeps a
    // single route from sitting at the top of a map of eight pods.
    const top = PADDING + HEADER + (columnHeight(tallest) - columnHeight(list.length)) / 2
    list.forEach((node, index) => {
      const entry = { node, x, y: top + index * (NODE_HEIGHT + ROW_GAP) }
      placed.set(node.id, entry)
      nodes.push(entry)
    })
  }

  const edges: PlacedEdge[] = []
  for (const edge of map.edges) {
    const from = placed.get(edge.from)
    const to = placed.get(edge.to)
    if (!from || !to) continue
    const x1 = from.x + NODE_WIDTH
    const y1 = from.y + NODE_HEIGHT / 2
    const x2 = to.x
    const y2 = to.y + NODE_HEIGHT / 2
    const bend = Math.max(24, (x2 - x1) / 2)
    // The label sits in the first gap the edge crosses, centred on it — an
    // edge that skips a column (Service straight to a pod) must not put its
    // label on top of the column it skipped.
    const gap = (left.get(to.node.column) ?? x2) - x1
    const firstGap = Math.min(gap, labelledFrom.has(from.node.column) ? LABEL_GAP : COLUMN_GAP)
    edges.push({
      edge,
      path: `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`,
      labelX: x1 + firstGap / 2,
      labelY: x2 - x1 > firstGap ? y1 : (y1 + y2) / 2,
      labelWidth: firstGap - 12,
    })
  }

  const columns = used.map((column) => ({
    title: COLUMN_TITLES[column] ?? '',
    x: left.get(column) ?? PADDING,
  }))

  return { width, height, columns, nodes, edges }
}

/**
 * What lights up when a node is pointed at: everything upstream of it and
 * everything downstream, walked separately. Walking both ways from every node
 * reached would light the whole map through any shared Service — pointing at
 * one pod has to show that pod's path, not every route's.
 */
export function tracePath(map: TrafficMap, id: string): { nodes: Set<string>; edges: Set<TrafficEdge> } {
  const nodes = new Set<string>([id])
  const edges = new Set<TrafficEdge>()
  const walk = (start: string, forward: boolean) => {
    const queue = [start]
    while (queue.length > 0) {
      const current = queue.shift() as string
      for (const edge of map.edges) {
        const [here, there] = forward ? [edge.from, edge.to] : [edge.to, edge.from]
        if (here !== current) continue
        edges.add(edge)
        if (!nodes.has(there)) {
          nodes.add(there)
          queue.push(there)
        }
      }
    }
  }
  walk(id, true)
  walk(id, false)
  return { nodes, edges }
}

export interface TrafficProblem {
  /** The node to open or point at for this problem. */
  id: string
  /** What the problem is about, in words: "Service shop/api", "web → api". */
  subject: string
  problem: string
  state: TrafficState
}

const RANK: Record<TrafficState, number> = {
  bad: 0,
  denied: 1,
  outside: 2,
  warn: 3,
  unchecked: 4,
  ok: 5,
}

/** Whether a state is something the reader should be told about. */
export function isProblem(state: TrafficState, problem?: string): boolean {
  return state === 'bad' || state === 'warn' || state === 'denied' || state === 'outside' ||
    (state === 'unchecked' && Boolean(problem))
}

function subjectOf(node: TrafficNode | undefined, fallback: string): string {
  if (!node) return fallback
  return node.namespace ? `${node.kind} ${node.namespace}/${node.name}` : `${node.kind} ${node.name}`
}

/**
 * Every hop with something to say, worst first. This is the map in words: the
 * list a screen reader reads and the one somebody pastes into an incident.
 */
export function trafficProblems(map: TrafficMap): TrafficProblem[] {
  const byId = new Map(map.nodes.map((node) => [node.id, node]))
  const out: TrafficProblem[] = []
  for (const node of map.nodes) {
    if (!isProblem(node.state, node.problem)) continue
    out.push({
      id: node.id,
      subject: subjectOf(node, node.id),
      problem: node.problem ?? defaultProblem(node.state),
      state: node.state,
    })
  }
  for (const edge of map.edges) {
    // An edge only has its own problem when it is a different one from its
    // target's — a missing port, say. "Sends to a Service that does not exist"
    // is already said by the Service.
    if (!edge.problem || !isProblem(edge.state, edge.problem)) continue
    const target = byId.get(edge.to)
    if (target?.problem === edge.problem) continue
    out.push({
      id: edge.to,
      subject: `${byId.get(edge.from)?.name ?? edge.from} → ${target?.name ?? edge.to}`,
      problem: edge.problem,
      state: edge.state,
    })
  }
  return out.sort((a, b) => RANK[a.state] - RANK[b.state])
}

function defaultProblem(state: TrafficState): string {
  switch (state) {
    case 'denied':
      return 'the cluster refused this read'
    case 'outside':
      return 'outside your granted scope'
    default:
      return ''
  }
}
