import { describe, expect, it } from 'vitest'

import type { CapacityDimension, NodeCapacity } from '../api/types'
import { HEAT_COLUMNS, groupNodes, heatLevel, heatValue, qosLine, sortNodes } from './capacityHeatmap'
import type { HeatKey } from './capacityHeatmap'

function dimension(over: Partial<CapacityDimension> = {}): CapacityDimension {
  return {
    allocatable: 4000,
    requested: 0,
    limited: 0,
    used: 0,
    requested_percent: 0,
    limited_percent: 0,
    used_percent: 0,
    unlimited_containers: 0,
    ...over,
  }
}

function node(name: string, over: Partial<NodeCapacity> = {}): NodeCapacity {
  return {
    name,
    roles: ['worker'],
    ready: true,
    schedulable: true,
    taints: [],
    placeable: true,
    cpu: dimension(),
    memory: dimension({ allocatable: 8 << 30 }),
    pods: { allocatable: 110, scheduled: 0, percent: 0, without_requests: 0 },
    qos: { guaranteed: 0, burstable: 0, best_effort: 0 },
    headroom: { cpu: 4000, memory: 8 << 30, pods: 110 },
    concerns: [],
    severity: 'ok',
    top_requests: [],
    borrowing_pods: 0,
    top_borrowers: [],
    ...over,
  }
}

function column(key: HeatKey) {
  const found = HEAT_COLUMNS.find((entry) => entry.key === key)
  if (!found) throw new Error(`no column ${key}`)
  return found
}

describe('heatLevel', () => {
  it('turns amber and rust exactly where the server writes a concern', () => {
    const reserved = column('cpu_requested')
    expect(heatLevel(89.9, reserved)).toBe('heat-3')
    expect(heatLevel(90, reserved)).toBe('warn')
    expect(heatLevel(100, reserved)).toBe('danger')
    expect(heatLevel(130, column('pods'))).toBe('danger')
  })

  it('never warns on a column the server raises nothing for', () => {
    // A CPU shortage honours every request, so the server writes a note, not a
    // warning — and a cell that turned amber would be a claim with nothing behind it.
    expect(heatLevel(99, column('cpu_used'))).toBe('heat-3')
    expect(heatLevel(95, column('memory_used'))).toBe('warn')
  })

  it('measures limits against their own threshold, which differs by resource', () => {
    expect(heatLevel(150, column('cpu_limited'))).toBe('heat-3')
    expect(heatLevel(200, column('cpu_limited'))).toBe('warn')
    expect(heatLevel(100, column('memory_limited'))).toBe('warn')
    // Limits never reach rust: overcommitment is contention, not exhaustion.
    expect(heatLevel(900, column('memory_limited'))).toBe('warn')
  })

  it('shades density in quarters of the way to the first threshold', () => {
    const reserved = column('memory_requested')
    expect(heatLevel(0, reserved)).toBe('plain')
    expect(heatLevel(22, reserved)).toBe('plain')
    expect(heatLevel(23, reserved)).toBe('heat-1')
    expect(heatLevel(45, reserved)).toBe('heat-2')
    expect(heatLevel(68, reserved)).toBe('heat-3')
  })

  it('draws an unknown as unknown, never as an empty cell', () => {
    expect(heatLevel(null, column('cpu_requested'))).toBe('unknown')
  })
})

describe('heatValue', () => {
  it('reads usage only where the Metrics API answered for the node', () => {
    const measured = node('n1', { cpu: dimension({ used: 900, used_percent: 22.5 }) })
    expect(heatValue(measured, 'cpu_used', true)).toBe(22.5)
    expect(heatValue(measured, 'cpu_used', false)).toBeNull()
    // Left out by the Metrics API: 0 is "not measured", not "idle".
    expect(heatValue(node('n2'), 'cpu_used', true)).toBeNull()
  })

  it('cannot answer for a node that reports no allocatable', () => {
    const blind = node('n1', { cpu: dimension({ allocatable: 0 }) })
    expect(heatValue(blind, 'cpu_requested', true)).toBeNull()
    expect(heatValue(blind, 'cpu_limited', true)).toBeNull()
  })

  it('writes a limit past the node as the number it is', () => {
    const over = node('n1', { cpu: dimension({ limited_percent: 340 }) })
    expect(heatValue(over, 'cpu_limited', true)).toBe(340)
  })
})

describe('sortNodes', () => {
  const full = node('b-full', {
    severity: 'danger',
    cpu: dimension({ requested_percent: 100, used: 3000, used_percent: 75 }),
  })
  const half = node('a-half', {
    severity: 'note',
    cpu: dimension({ requested_percent: 50, used: 1000, used_percent: 25 }),
  })
  const unmeasured = node('c-unmeasured', { cpu: dimension({ requested_percent: 10 }) })

  it('puts the fullest first, and what cannot answer last', () => {
    const order = sortNodes([unmeasured, half, full], 'cpu_used', true).map((entry) => entry.name)
    expect(order).toEqual(['b-full', 'a-half', 'c-unmeasured'])
  })

  it('orders by severity, then name', () => {
    const order = sortNodes([unmeasured, half, full], 'severity', true).map((entry) => entry.name)
    expect(order).toEqual(['b-full', 'a-half', 'c-unmeasured'])
  })

  it('orders by name ascending, and leaves its input alone', () => {
    const input = [full, unmeasured, half]
    expect(sortNodes(input, 'name', true).map((entry) => entry.name)).toEqual([
      'a-half',
      'b-full',
      'c-unmeasured',
    ])
    expect(input[0].name).toBe('b-full')
  })
})

describe('groupNodes', () => {
  it('puts the control plane first and keeps the arriving order within a group', () => {
    const groups = groupNodes([
      node('w2'),
      node('cp1', { roles: ['control-plane'] }),
      node('gpu1', { roles: ['gpu'] }),
      node('w1'),
      node('old-master', { roles: ['master'] }),
    ])
    expect(groups.map((group) => group.role)).toEqual(['control-plane', 'gpu', 'worker'])
    expect(groups[0].nodes.map((entry) => entry.name)).toEqual(['cp1', 'old-master'])
    expect(groups[2].nodes.map((entry) => entry.name)).toEqual(['w2', 'w1'])
  })
})

describe('qosLine', () => {
  it('names only the classes present, in a fixed order', () => {
    expect(qosLine({ guaranteed: 0, burstable: 3, best_effort: 2 })).toBe('3 Burstable · 2 BestEffort')
    expect(qosLine({ guaranteed: 0, burstable: 0, best_effort: 0 })).toBe('')
  })
})
