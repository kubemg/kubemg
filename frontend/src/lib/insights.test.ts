import { describe, expect, it } from 'vitest'

import type { Pod, PodContainer } from '../api/types'
import { MAX_ALERTS, matchesPodBucket, podBucket, podFailureReason, podInsights } from './insights'

/*
 * The bucketing is the one derivation two surfaces have to agree on: the pod
 * list in Explore and the pod table inside a workload's drawer both read it, so
 * a change here that makes "Running" mean something else changes both at once —
 * which is the point, and is why it is asserted here rather than looked at in
 * one of them.
 */

function container(state: string, ready = true): PodContainer {
  return {
    name: 'app',
    image: 'nginx:1.27',
    ready,
    restarts: 0,
    state,
    cpu_request_millicores: 0,
    cpu_limit_millicores: 0,
    memory_request_bytes: 0,
    memory_limit_bytes: 0,
  }
}

function pod(over: Partial<Pod> = {}): Pod {
  return {
    name: 'web-1',
    namespace: 'default',
    phase: 'Running',
    node: 'node-1',
    ready: 1,
    total: 1,
    restarts: 0,
    created_at: '2026-01-01T00:00:00Z',
    containers: [container('Running')],
    ...over,
  }
}

describe('podBucket', () => {
  it('puts every pod in exactly one bucket, off phase and readiness', () => {
    expect(podBucket(pod())).toBe('running')
    expect(podBucket(pod({ phase: 'Pending' }))).toBe('pending')
    expect(podBucket(pod({ phase: 'Failed' }))).toBe('failed')
    expect(podBucket(pod({ phase: 'Succeeded' }))).toBe('succeeded')
    expect(podBucket(pod({ phase: 'Terminating' }))).toBe('unknown')
    expect(podBucket(pod({ phase: '' }))).toBe('unknown')
  })

  it('does not call a Running pod with a failing readiness probe healthy', () => {
    // The state a phase-only summary gets wrong: Running forever, serving
    // nothing.
    expect(podBucket(pod({ ready: 0, total: 1 }))).toBe('notready')
    expect(podBucket(pod({ ready: 1, total: 2 }))).toBe('notready')
  })
})

describe('matchesPodBucket', () => {
  it('crosses the partition for restarts rather than dividing it', () => {
    // A crash-looping pod is Running between restarts, so it is in both.
    const looping = pod({ restarts: 7 })
    expect(matchesPodBucket(looping, 'running')).toBe(true)
    expect(matchesPodBucket(looping, 'restarting')).toBe(true)
    expect(matchesPodBucket(pod(), 'restarting')).toBe(false)
  })

  it('matches everything for all, and nothing for a workload bucket', () => {
    // A caller holding one selection for whichever list is open never has to
    // cast: the wrong half of the union narrows to nothing, truthfully.
    expect(matchesPodBucket(pod(), 'all')).toBe(true)
    expect(matchesPodBucket(pod(), 'degraded')).toBe(false)
  })
})

describe('podFailureReason', () => {
  it('names the failing container state, and only a failing one', () => {
    expect(podFailureReason(pod({ containers: [container('CrashLoopBackOff')] }))).toBe(
      'CrashLoopBackOff',
    )
    expect(podFailureReason(pod({ containers: [container('ImagePullBackOff')] }))).toBe(
      'ImagePullBackOff',
    )
    expect(podFailureReason(pod())).toBeNull()
    // Completed is not a failure, however un-Running it reads.
    expect(podFailureReason(pod({ containers: [container('Completed')] }))).toBeNull()
  })

  it('finds the failure behind a healthy sidecar', () => {
    const mixed = pod({ containers: [container('Running'), container('OOMKilled', false)] })
    expect(podFailureReason(mixed)).toBe('OOMKilled')
  })
})

describe('podInsights', () => {
  it('says so plainly when there is nothing here', () => {
    const insight = podInsights([], null)
    expect(insight.headline).toBe('Nothing running here')
    expect(insight.segments).toEqual([])
    expect(insight.total.value).toBe(0)
  })

  it('partitions the list, leaving empty buckets out', () => {
    const insight = podInsights(
      [pod(), pod({ name: 'web-2' }), pod({ name: 'web-3', ready: 0 })],
      null,
    )
    expect(insight.segments.map((segment) => [segment.id, segment.value])).toEqual([
      ['running', 2],
      ['notready', 1],
    ])
    // A partition sums to the whole.
    expect(insight.segments.reduce((sum, segment) => sum + segment.value, 0)).toBe(3)
    expect(insight.headline).toBe('1 pod not running normally')
    expect(insight.headlineTone).toBe('warn')
  })

  it('reads a failure as bad and a healthy list as ok', () => {
    expect(podInsights([pod(), pod({ name: 'web-2' })], null).headline).toBe(
      'All 2 pods are running',
    )
    expect(podInsights([pod(), pod({ name: 'web-2' })], null).headlineTone).toBe('ok')
    expect(podInsights([pod({ phase: 'Failed' })], null).headlineTone).toBe('bad')
  })

  it('keeps restarts a reading rather than a segment', () => {
    const insight = podInsights([pod({ restarts: 3 }), pod({ name: 'web-2', restarts: 1 })], null)
    expect(insight.segments.map((segment) => segment.id)).toEqual(['running'])
    const restarting = insight.readings.find((reading) => reading.id === 'restarting')
    // The value counts restarts and the detail counts pods, because one restart
    // across forty pods is a rollout and forty on one pod is a crash loop.
    expect(restarting?.value).toBe(4)
    expect(restarting?.detail).toBe('across 2 pods')
    // Everything is Running, so the headline is not an outage — but it is not
    // silent either.
    expect(insight.headlineTone).toBe('warn')
    expect(insight.summary).toContain('4 restarts')
  })

  it('caps the alert list and puts the worst first', () => {
    const pods = [
      pod({ name: 'a', phase: 'Pending' }),
      pod({ name: 'b', containers: [container('CrashLoopBackOff')] }),
      pod({ name: 'c', ready: 0 }),
      pod({ name: 'd', phase: 'Failed' }),
      pod({ name: 'e', phase: 'Pending' }),
      pod({ name: 'f', phase: 'Pending' }),
      pod({ name: 'g', phase: 'Pending' }),
    ]
    const insight = podInsights(pods, null)
    expect(insight.alerts.length).toBe(MAX_ALERTS)
    // Every pod that is not fine is counted even where it is not listed.
    expect(insight.alerting).toBe(7)
    expect(insight.alerts[0].tone).toBe('bad')
  })

  it('aggregates only the pods a live sample actually covers', () => {
    const usage = new Map([
      [
        'default/web-1',
        {
          name: 'web-1',
          namespace: 'default',
          cpu_millicores: 120,
          memory_bytes: 64 * 1024 * 1024,
          containers: [],
        },
      ],
    ])
    const insight = podInsights([pod(), pod({ name: 'web-2' })], usage)
    expect(insight.usage).toEqual({ cpu: 120, memory: 64 * 1024 * 1024, sampled: 1 })
    // No sample at all is absent, not zero.
    expect(podInsights([pod()], new Map()).usage).toBeUndefined()
  })
})
