import { describe, expect, it } from 'vitest'

import { completeCapacity } from './capacity'
import type { ClusterCapacity } from './types'

/*
 * The report a server from before the heatmap answers with: no taints, no QoS,
 * no headroom, no borrowers, no placement. Rendered as it arrived, the page read
 * `node.taints.length` off undefined and drew nothing at all.
 */
const previousServer = {
  available: true,
  nodes: [
    {
      name: 'minikube',
      roles: ['control-plane'],
      ready: true,
      schedulable: true,
      cpu: {
        allocatable: 12000,
        requested: 2100,
        limited: 3200,
        used: 900,
        requested_percent: 17.5,
        limited_percent: 26.7,
        used_percent: 7.5,
        unlimited_containers: 3,
      },
      concerns: [],
      severity: 'ok',
      top_requests: [],
    },
  ],
  summary: { nodes: 1, ready: 1, schedulable: 1, severity_counts: { ok: 1 } },
  unscheduled: [],
  unscheduled_pods: 0,
} as unknown as Partial<ClusterCapacity>

describe('completeCapacity', () => {
  it('fills every field the page reads into, without touching what arrived', () => {
    const report = completeCapacity(previousServer)
    const node = report.nodes[0]

    expect(node.taints).toEqual([])
    expect(node.top_borrowers).toEqual([])
    expect(node.qos).toEqual({ guaranteed: 0, burstable: 0, best_effort: 0 })
    expect(node.memory.allocatable).toBe(0)
    expect(node.cpu.requested).toBe(2100)
    expect(node.ready).toBe(true)

    expect(report.summary.placement.largest_cpu).toBeNull()
    expect(report.summary.qos.burstable).toBe(0)
    expect(report.summary.nodes).toBe(1)
    expect(report.pod_usage_available).toBe(false)
  })

  it('never counts a node as placeable on a server that did not say so', () => {
    expect(completeCapacity(previousServer).nodes[0].placeable).toBe(false)
  })

  it('answers an empty body with an empty report', () => {
    const report = completeCapacity({})
    expect(report.nodes).toEqual([])
    expect(report.summary.placement.placeable_nodes).toBe(0)
    expect(report.available).toBe(false)
  })
})
