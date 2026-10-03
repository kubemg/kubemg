import { describe, expect, it } from 'vitest'

import type { AuditEvent, Cluster } from '../api/types'
import { activityLine, activityRefused, fleetLinkCounts } from './fleet'

function cluster(over: Partial<Cluster> = {}): Cluster {
  return {
    id: 1,
    name: 'edge-us',
    environment: 'prod',
    api_url: '',
    status: 'healthy',
    created_at: '2026-01-01T00:00:00Z',
    k8s_role: 'edit',
    namespaces: [],
    connection_mode: 'agent',
    agent_attached: true,
    ...over,
  }
}

function event(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 1,
    at: '2026-01-01T00:00:00Z',
    user_id: 1,
    username: 'ayse',
    cluster_id: 1,
    cluster: 'prod-eu-1',
    verb: 'get',
    method: 'GET',
    path: '/api/v1/namespaces/payments/pods',
    impersonated_groups: [],
    status: 200,
    duration_ms: 12,
    streaming: false,
    ...over,
  }
}

describe('fleetLinkCounts', () => {
  it('buckets each cluster by its own link state, keeping every bucket', () => {
    const counts = fleetLinkCounts([
      cluster({ id: 1, agent_attached: true }),
      cluster({ id: 2, agent_attached: true }),
      cluster({ id: 3, agent_attached: false, status: 'pending' }),
      cluster({ id: 4, agent_attached: false, status: 'unhealthy' }),
    ])
    expect(counts).toEqual({ live: 2, direct: 0, idle: 1, down: 1 })
  })

  it('counts a direct-mode cluster as direct whatever its health', () => {
    expect(fleetLinkCounts([cluster({ connection_mode: 'direct' })]).direct).toBe(1)
  })
})

describe('activityLine', () => {
  it('names the namespaced object when the record has one', () => {
    expect(activityLine(event({ verb: 'exec', namespace: 'payments', resource: 'pods' }))).toBe(
      'exec · payments/pods · prod-eu-1',
    )
  })

  it('falls back to the path when the record names no resource', () => {
    expect(activityLine(event({ verb: 'list' }))).toBe(
      'list · /api/v1/namespaces/payments/pods · prod-eu-1',
    )
  })

  it('leaves out a cluster the record does not carry', () => {
    expect(activityLine(event({ verb: 'replay', resource: 'recording', cluster: '' }))).toBe(
      'replay · recording',
    )
  })
})

describe('activityRefused', () => {
  it('flags a refusal and an error, and nothing else', () => {
    expect(activityRefused(event({ status: 403 }))).toBe(true)
    expect(activityRefused(event({ status: 200, error: 'stream reset' }))).toBe(true)
    expect(activityRefused(event({ status: 201 }))).toBe(false)
  })
})
