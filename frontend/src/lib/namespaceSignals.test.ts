import { describe, expect, it } from 'vitest'

import type { Pod, PodContainer, PodUsage } from '../api/types'
import {
  MAX_SLICES,
  consumption,
  imagePullSignal,
  limitSignal,
  restartSignal,
  throttleSignal,
  workloadOf,
} from './namespaceSignals'
import { podUsageIndex } from './units'

function container(over: Partial<PodContainer> = {}): PodContainer {
  return {
    name: 'app',
    image: 'app:1',
    ready: true,
    restarts: 0,
    state: 'running',
    cpu_request_millicores: 0,
    cpu_limit_millicores: 0,
    memory_request_bytes: 0,
    memory_limit_bytes: 0,
    ...over,
  }
}

function pod(name: string, over: Partial<Pod> = {}): Pod {
  return {
    name,
    namespace: 'shop',
    phase: 'Running',
    node: 'node-1',
    ready: 1,
    total: 1,
    restarts: 0,
    created_at: '2026-01-01T00:00:00Z',
    containers: [container()],
    ephemeral_containers: [],
    ...over,
  }
}

function sample(name: string, cpu: number, memory: number, containers?: PodUsage['containers']): PodUsage {
  return {
    name,
    namespace: 'shop',
    cpu_millicores: cpu,
    memory_bytes: memory,
    containers: containers ?? [{ name: 'app', cpu_millicores: cpu, memory_bytes: memory }],
  }
}

describe('workloadOf', () => {
  it('names the owner, and the pod itself when nothing owns it', () => {
    expect(workloadOf(pod('a-1', { owner: { kind: 'Deployment', name: 'a' } }))).toEqual({
      kind: 'Deployment',
      name: 'a',
    })
    expect(workloadOf(pod('scratch'))).toEqual({ kind: 'Pod', name: 'scratch' })
  })
})

describe('consumption', () => {
  it('is null without a live sample, never a namespace that spends nothing', () => {
    expect(consumption([pod('a')], null, 'cpu')).toBeNull()
    expect(consumption([pod('a')], podUsageIndex([]), 'cpu')).toBeNull()
  })

  it('sums replicas into their workload and ranks the workloads', () => {
    const pods = [
      pod('api-1', { owner: { kind: 'Deployment', name: 'api' } }),
      pod('api-2', { owner: { kind: 'Deployment', name: 'api' } }),
      pod('db-0', { owner: { kind: 'StatefulSet', name: 'db' } }),
      pod('unsampled', { owner: { kind: 'Deployment', name: 'api' } }),
    ]
    const usage = podUsageIndex([
      sample('api-1', 100, 10),
      sample('api-2', 50, 10),
      sample('db-0', 250, 500),
    ])

    const cpu = consumption(pods, usage, 'cpu')!
    expect(cpu.total).toBe(400)
    expect(cpu.sampled).toBe(3)
    expect(cpu.pods).toBe(4)
    expect(cpu.slices.map((s) => [s.label, s.value, s.pods, s.slot])).toEqual([
      ['db', 250, 1, 0],
      ['api', 150, 2, 1],
    ])
    expect(cpu.slices[0].share).toBeCloseTo(0.625)

    const memory = consumption(pods, usage, 'memory')!
    expect(memory.slices[0]).toMatchObject({ label: 'db', value: 500 })
  })

  it('folds the tail into the eighth slot and never invents a ninth colour', () => {
    const pods = Array.from({ length: 11 }, (_, i) => pod(`w${i}`))
    const usage = podUsageIndex(pods.map((p, i) => sample(p.name, 100 - i, 1)))
    const result = consumption(pods, usage, 'cpu')!

    expect(result.slices).toHaveLength(MAX_SLICES)
    expect(result.workloads).toBe(11)
    const rest = result.slices[MAX_SLICES - 1]
    expect(rest).toMatchObject({ key: 'rest', label: '4 more', slot: MAX_SLICES - 1, pods: 4 })
    expect(Math.max(...result.slices.map((s) => s.slot))).toBe(MAX_SLICES - 1)
    expect(result.slices.reduce((sum, s) => sum + s.share, 0)).toBeCloseTo(1)
  })
})

describe('restartSignal', () => {
  it('counts pods and restarts, most first, and calls out a crash loop', () => {
    const result = restartSignal([
      pod('calm'),
      pod('few', { restarts: 2 }),
      pod('many', { restarts: 15, containers: [container({ state: 'CrashLoopBackOff' })] }),
    ])
    expect(result.count).toBe(2)
    expect(result.restarts).toBe(17)
    expect(result.pods.map((p) => [p.name, p.tone])).toEqual([
      ['many', 'bad'],
      ['few', 'warn'],
    ])
    expect(result.pods[0].detail).toContain('CrashLoopBackOff')
  })
})

describe('imagePullSignal', () => {
  it('names the kubelet reason and ignores every other waiting state', () => {
    const result = imagePullSignal([
      pod('pulling', { containers: [container(), container({ name: 'side', state: 'ErrImagePull' })] }),
      pod('backoff', { containers: [container({ state: 'ImagePullBackOff' })] }),
      pod('creating', { containers: [container({ state: 'ContainerCreating' })] }),
    ])
    expect(result.count).toBe(2)
    expect(result.pods.map((p) => [p.name, p.detail])).toEqual([
      ['backoff', 'ImagePullBackOff'],
      ['pulling', 'ErrImagePull'],
    ])
  })
})

describe('limitSignal', () => {
  const limited = (over: Partial<PodContainer> = {}) =>
    container({ cpu_limit_millicores: 200, memory_limit_bytes: 1000, ...over })

  it('leads with a previous OOM kill even when the pod is running now', () => {
    const result = limitSignal(
      [pod('oom', { containers: [limited({ last_termination_reason: 'OOMKilled' })] })],
      null,
    )
    expect(result.pods).toEqual([
      expect.objectContaining({ name: 'oom', detail: 'OOMKilled', tone: 'bad' }),
    ])
    expect(result.measured).toBe(false)
  })

  it('compares each container against its own limit, not the pod sum', () => {
    const pods = [
      pod('hot', {
        containers: [limited(), limited({ name: 'side', memory_limit_bytes: 0, cpu_limit_millicores: 0 })],
      }),
      pod('cpu', { containers: [limited()] }),
      pod('fine', { containers: [limited()] }),
      pod('nolimit'),
    ]
    const usage = podUsageIndex([
      sample('hot', 10, 5000, [
        { name: 'app', cpu_millicores: 10, memory_bytes: 950 },
        { name: 'side', cpu_millicores: 0, memory_bytes: 4050 },
      ]),
      sample('cpu', 198, 100),
      sample('fine', 20, 100),
      sample('nolimit', 5000, 5000),
    ])
    const result = limitSignal(pods, usage)

    expect(result.limited).toBe(3)
    expect(result.pods.map((p) => [p.name, p.detail, p.tone])).toEqual([
      ['cpu', 'CPU 99%', 'warn'],
      ['hot', 'memory 95%', 'bad'],
    ])
  })
})

describe('throttleSignal', () => {
  it('keeps only pods past the alerting threshold, worst first', () => {
    const result = throttleSignal(
      [
        { name: 'b', current: 0.3 },
        { name: 'a', current: 0.8 },
        { name: 'quiet', current: 0.1 },
        { name: 'nan', current: Number.NaN },
      ],
      'shop',
    )
    expect(result.pods.map((p) => [p.name, p.detail, p.tone])).toEqual([
      ['a', '80%', 'bad'],
      ['b', '30%', 'warn'],
    ])
  })
})
