import { describe, expect, it } from 'vitest'

import type { TrafficEdge, TrafficMap, TrafficNode } from '../api/types'
import { NODE_HEIGHT, layoutTraffic, problemPaths, tracePath, trafficProblems } from './trafficMap'

function node(id: string, column: number, over: Partial<TrafficNode> = {}): TrafficNode {
  return { id, kind: 'Service', name: id, column, detail: [], state: 'ok', ...over }
}

function edge(from: string, to: string, over: Partial<TrafficEdge> = {}): TrafficEdge {
  return { from, to, labels: [], state: 'ok', ...over }
}

/*
 * One route with two backends, each with its own workload and pod:
 *
 *   host ─ web ─┬─ api ─ api-deploy ─ api-pod
 *               └─ cart ─ cart-deploy ─ cart-pod
 */
function fork(): TrafficMap {
  return {
    root: 'web',
    nodes: [
      node('host', 0, { kind: 'Host' }),
      node('web', 1, { kind: 'Ingress' }),
      node('cart', 2),
      node('api', 2),
      node('api-deploy', 3, { kind: 'Deployment' }),
      node('cart-deploy', 3, { kind: 'Deployment' }),
      node('api-pod', 4, { kind: 'Pod' }),
      node('cart-pod', 4, { kind: 'Pod' }),
    ],
    edges: [
      edge('host', 'web'),
      edge('web', 'api'),
      edge('web', 'cart'),
      edge('api', 'api-deploy'),
      edge('cart', 'cart-deploy'),
      edge('api-deploy', 'api-pod'),
      edge('cart-deploy', 'cart-pod'),
    ],
    notes: [],
  }
}

describe('laying a traffic map out', () => {
  it('drops a column nothing is in rather than drawing a gap', () => {
    const map: TrafficMap = {
      root: 'api',
      nodes: [node('api', 2), node('pod', 4, { kind: 'Pod' })],
      edges: [edge('api', 'pod')],
      notes: [],
    }
    const layout = layoutTraffic(map)
    expect(layout.columns.map((column) => column.title)).toEqual(['Service', 'Pods'])
    const [service, pod] = layout.nodes
    expect(service.x).toBeLessThan(pod.x)
  })

  it('keeps each backend on the row of the path that leads to it', () => {
    const layout = layoutTraffic(fork())
    const y = (id: string) => layout.nodes.find((entry) => entry.node.id === id)?.y ?? NaN
    // `cart` was listed first in its column, but `api` is the first edge out of
    // the route — what matters is that each path runs straight, not the list's
    // own order.
    expect(y('api-deploy') < y('cart-deploy')).toBe(y('api') < y('cart'))
    expect(y('api-pod') < y('cart-pod')).toBe(y('api') < y('cart'))
  })

  it('centres a short column against the tallest', () => {
    const layout = layoutTraffic(fork())
    const y = (id: string) => layout.nodes.find((entry) => entry.node.id === id)?.y ?? NaN
    const middle = (y('api') + y('cart') + NODE_HEIGHT) / 2
    expect(y('web') + NODE_HEIGHT / 2).toBeCloseTo(middle)
  })

  it('draws an edge only between nodes it placed', () => {
    const map = fork()
    map.edges.push(edge('web', 'nowhere'))
    expect(layoutTraffic(map).edges).toHaveLength(7)
  })
})

describe('tracing one hop’s path', () => {
  it('lights everything upstream and downstream of a pod', () => {
    const { nodes } = tracePath(fork(), 'api-pod')
    expect([...nodes].sort()).toEqual(['api', 'api-deploy', 'api-pod', 'host', 'web'])
  })

  it('does not light a sibling backend through the route they share', () => {
    const { nodes } = tracePath(fork(), 'api')
    expect(nodes.has('cart')).toBe(false)
    expect(nodes.has('cart-pod')).toBe(false)
    expect(nodes.has('host')).toBe(true)
  })

  it('lights the whole fan-out from the route', () => {
    const { nodes, edges } = tracePath(fork(), 'web')
    expect(nodes.size).toBe(8)
    expect(edges.size).toBe(7)
  })
})

describe('the map in words', () => {
  it('lists broken hops before degraded ones, and says nothing about healthy ones', () => {
    const map = fork()
    map.nodes[3] = node('api', 2, { state: 'warn', problem: '1 of 3 endpoints are not ready' })
    map.nodes[2] = node('cart', 2, { state: 'bad', problem: 'its selector app=cart matches no pods' })
    const problems = trafficProblems(map)
    expect(problems.map((entry) => entry.id)).toEqual(['cart', 'api'])
  })

  it('reports an edge’s own problem, but not one its target already states', () => {
    const map = fork()
    map.nodes[3] = node('api', 2, { state: 'bad', problem: 'this Service does not exist' })
    map.edges[1] = edge('web', 'api', { state: 'bad', problem: 'this Service does not exist' })
    map.edges[2] = edge('web', 'cart', { state: 'bad', problem: 'port 8080 is not a port of Service cart' })
    const problems = trafficProblems(map)
    expect(problems).toHaveLength(2)
    expect(problems.find((entry) => entry.problem.startsWith('port'))?.subject).toBe('web → cart')
  })

  it('reports a hop outside the grant, but not a node drawn without reading', () => {
    const map = fork()
    map.nodes.push(node('ledger', 2, { state: 'outside', problem: 'namespace payments is outside your granted scope' }))
    map.nodes.push(node('mesh', 0, { kind: 'Mesh', state: 'unchecked' }))
    const ids = trafficProblems(map).map((entry) => entry.id)
    expect(ids).toContain('ledger')
    expect(ids).not.toContain('mesh')
  })
})

describe('narrowing to what needs a look', () => {
  it('keeps a broken hop and every path through it, and drops the healthy branch', () => {
    const map = fork()
    map.nodes[2] = node('cart', 2, { state: 'bad', problem: 'its selector app=cart matches no pods' })
    const ids = problemPaths(map).nodes.map((entry) => entry.id).sort()
    expect(ids).toEqual(['cart', 'cart-deploy', 'cart-pod', 'host', 'web'])
  })

  it('keeps the source of an edge that is itself the problem', () => {
    const map = fork()
    map.edges[1] = edge('web', 'api', { state: 'bad', problem: 'port 9443 is not a port of Service api' })
    const ids = problemPaths(map).nodes.map((entry) => entry.id)
    expect(ids).toContain('web')
    expect(ids).toContain('api')
  })

  it('uses the map’s own column titles', () => {
    const map: TrafficMap = { ...fork(), columns: ['Workload', 'Uses', 'Bound volume', '', ''] }
    expect(layoutTraffic(map).columns[0].title).toBe('Workload')
  })
})
