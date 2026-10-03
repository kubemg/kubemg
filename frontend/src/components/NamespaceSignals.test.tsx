/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Cluster, Pod, PodContainer } from '../api/types'
import { podUsageIndex } from '../lib/units'
import { NamespaceSignals } from './NamespaceSignals'

// The throttling cell is the one read this block makes over the pod list; on a
// cluster with no datasource it must say so rather than claim "none".
vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    compareMetrics: vi.fn(() => Promise.reject(new Error('no datasource'))),
    unconfigured: () => true,
    fetchPods: vi.fn(() => Promise.reject(new Error('must not read pods it was handed'))),
  }
})

const cluster = { id: 7, name: 'minikube' } as Cluster

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
    node: 'n1',
    ready: 1,
    total: 1,
    restarts: 0,
    created_at: '2026-01-01T00:00:00Z',
    containers: [container()],
    ephemeral_containers: [],
    ...over,
  }
}

const pods = [
  pod('api-1', { owner: { kind: 'Deployment', name: 'api' }, restarts: 12 }),
  pod('api-2', { owner: { kind: 'Deployment', name: 'api' } }),
  pod('worker-1', {
    owner: { kind: 'Deployment', name: 'worker' },
    containers: [container({ state: 'ImagePullBackOff' })],
  }),
  pod('db-0', {
    owner: { kind: 'StatefulSet', name: 'db' },
    containers: [container({ memory_limit_bytes: 100, last_termination_reason: 'OOMKilled' })],
  }),
]

const usage = podUsageIndex(
  [
    ['api-1', 300],
    ['api-2', 100],
    ['db-0', 600],
  ].map(([name, cpu]) => ({
    name: name as string,
    namespace: 'shop',
    cpu_millicores: cpu as number,
    memory_bytes: 10,
    containers: [],
  })),
)

afterEach(cleanup)

describe('the namespace block', () => {
  it('divides live usage per workload and names the pods behind each signal', async () => {
    const onOpenPod = vi.fn()
    const onRestarting = vi.fn()
    const { container: root } = render(
      <NamespaceSignals
        cluster={cluster}
        namespace="shop"
        loaded={{ pods, usage }}
        onOpenPod={onOpenPod}
        onRestarting={onRestarting}
      />,
    )

    // Replicas are summed into their workload: api is 400m of 1000m.
    expect(root.textContent).toContain('Consumption by workload')
    expect(root.textContent).toContain('1.00')
    const legend = screen.getByTitle('Deployment api — 2 pods')
    expect(legend.textContent).toContain('400m')
    expect(legend.textContent).toContain('40%')

    expect(root.textContent).toContain('ImagePullBackOff')
    expect(root.textContent).toContain('OOMKilled')

    fireEvent.click(screen.getByTitle('shop/worker-1 — ImagePullBackOff'))
    expect(onOpenPod).toHaveBeenCalledWith(pods[2])

    fireEvent.click(screen.getByTitle('Show only restarting pods'))
    expect(onRestarting).toHaveBeenCalled()

    expect(await screen.findByText(/No datasource/)).toBeTruthy()
  })

  it('says a namespace with no live sample is unmeasured, not idle', () => {
    const { container: root } = render(
      <NamespaceSignals
        cluster={cluster}
        namespace="shop"
        loaded={{ pods: [pod('a')], usage: null, usageReason: 'metrics-server is not installed' }}
        onOpenPod={() => {}}
      />,
    )
    expect(root.textContent).toContain('No live sample for this namespace.')
    expect(root.textContent).toContain('metrics-server is not installed')
    expect(root.textContent).toContain('No pod here declares a limit')
    expect(screen.queryByTitle('Show only restarting pods')).toBeNull()
  })
})
