import { describe, expect, it } from 'vitest'

import type { ResourceDescribeResult } from '../api/types'
import { conditionTone, glanceFor, itemCount, replicaTone } from './objectGlance'

function describeOf(
  spec: Record<string, string>,
  status: Record<string, string> = {},
): ResourceDescribeResult {
  const fields = (values: Record<string, string>) =>
    Object.entries(values).map(([path, value]) => ({ path, value }))
  return {
    kind: 'X',
    name: 'x',
    created_at: '2026-01-01T00:00:00Z',
    conditions: [],
    spec_summary: fields(spec),
    status_summary: fields(status),
    events: [],
    events_available: true,
  }
}

describe('glanceFor', () => {
  it('reads an absent ready count as zero, not as unknown', () => {
    const glance = glanceFor('deployments', describeOf({ replicas: '3' }))
    expect(glance?.replicas).toEqual({ desired: 3, ready: 0, noun: 'replica' })
    expect(glance?.bars.map((bar) => [bar.label, bar.value, bar.of])).toEqual([
      ['Up to date', 0, 3],
      ['Available', 0, 3],
    ])
  })

  it('takes the API server default for a desired size the spec leaves out', () => {
    expect(glanceFor('statefulsets', describeOf({}))?.replicas?.desired).toBe(1)
  })

  it('sizes a DaemonSet by the nodes it should run on', () => {
    const glance = glanceFor(
      'daemonsets',
      describeOf({}, { desiredNumberScheduled: '4', numberReady: '3', numberMisscheduled: '1' }),
    )
    expect(glance?.replicas).toEqual({ desired: 4, ready: 3, noun: 'node' })
    expect(glance?.chips).toContainEqual(
      expect.objectContaining({ label: 'Misscheduled', value: '1', tone: 'warn' }),
    )
  })

  it('splits a Job by outcome and counts what has not started', () => {
    const parts = glanceFor(
      'jobs',
      describeOf({ completions: '5' }, { succeeded: '2', active: '1', failed: '1' }),
    )?.segments?.parts
    expect(Object.fromEntries(parts?.map((part) => [part.key, part.value]) ?? [])).toEqual({
      succeeded: 2,
      active: 1,
      failed: 1,
      pending: 2,
    })
  })

  it('says a LoadBalancer is waiting for an address', () => {
    const chips = glanceFor('services', describeOf({ type: 'LoadBalancer', ports: '2 items' }))?.chips
    expect(chips).toContainEqual(expect.objectContaining({ label: 'Ports', value: '2' }))
    expect(chips).toContainEqual(
      expect.objectContaining({ label: 'Address', value: 'pending', tone: 'warn' }),
    )
  })

  it('does not ask about an address for a ClusterIP service', () => {
    const chips = glanceFor('services', describeOf({ type: 'ClusterIP' }))?.chips ?? []
    expect(chips.some((chip) => chip.label === 'Address')).toBe(false)
  })

  it('reads an ingress with no load-balancer entry as having no address', () => {
    const chips = glanceFor('ingresses', describeOf({ rules: '1 item' }))?.chips
    expect(chips).toContainEqual(
      expect.objectContaining({ label: 'Address', value: 'none yet', tone: 'warn' }),
    )
  })

  it('tones a claim by its phase', () => {
    const chips = glanceFor('persistentvolumeclaims', describeOf({}, { phase: 'Pending' }))?.chips
    expect(chips?.[0]).toEqual(expect.objectContaining({ label: 'Phase', tone: 'warn' }))
  })

  it('draws nothing for a kind it does not know', () => {
    expect(glanceFor('crd:example.com/v1/widgets', describeOf({ replicas: '3' }))).toBeNull()
  })
})

describe('itemCount', () => {
  it('reads the flatten’s count of a list of objects', () => {
    expect(itemCount('1 item')).toBe(1)
    expect(itemCount('12 items')).toBe(12)
    expect(itemCount('80, 443')).toBe(0)
    expect(itemCount(undefined)).toBe(0)
  })
})

describe('replicaTone', () => {
  it('reads all, some, none and zero apart', () => {
    expect(replicaTone(3, 3)).toBe('ok')
    expect(replicaTone(1, 3)).toBe('warn')
    expect(replicaTone(0, 3)).toBe('bad')
    expect(replicaTone(0, 0)).toBe('idle')
  })
})

describe('conditionTone', () => {
  it('knows which conditions are healthy False', () => {
    const at = { last_transition_at: undefined }
    expect(conditionTone({ type: 'Ready', status: 'True', ...at })).toBe('ok')
    expect(conditionTone({ type: 'MemoryPressure', status: 'False', ...at })).toBe('ok')
    expect(conditionTone({ type: 'MemoryPressure', status: 'True', ...at })).toBe('bad')
    expect(conditionTone({ type: 'Available', status: 'Unknown', ...at })).toBe('warn')
  })
})
