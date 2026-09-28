/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Cluster, DebugContainerResult, Pod } from '../api/types'
import { DebugContainerSheet } from './DebugContainerSheet'

/*
 * What is asserted here is the discipline the issue asked for: both facts an
 * operator cannot undo — no delete, shared namespaces — are on screen before
 * the button is reachable, the write addresses the container chosen from the
 * pod's own list, and the caller is handed the *new* container's name rather
 * than the one the session was asked against.
 *
 * A second discipline sits on top of the first: the write landing is not the
 * same moment as the container being attachable, so `onStarted` must not fire
 * — and no terminal must mount — until a poll of the pod's own status reports
 * the debug container running. A waiting reason (an image still pulling, or
 * one that never will) is shown rather than swallowed.
 */

const calls: unknown[][] = []
const podCalls: unknown[][] = []
let answer: () => Promise<DebugContainerResult> = async () => result()
// Every test but the ones about waiting wants the container to read as running
// on the very first poll, so that is the default rather than something each
// test has to arrange.
let podAnswer: () => Promise<Pod> = async () => podWith({ name: 'debug-abcd1234', running: true })

vi.mock('../api/client', () => ({
  debugPodContainer: (...args: unknown[]) => {
    calls.push(args)
    return answer()
  },
  fetchPod: (...args: unknown[]) => {
    podCalls.push(args)
    return podAnswer()
  },
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

const cluster = { id: 7, name: 'prod' } as Cluster

const pod: Pod = {
  name: 'checkout-7f9',
  namespace: 'shop',
  phase: 'Running',
  node: 'node-1',
  ready: 1,
  total: 1,
  restarts: 0,
  created_at: '2026-01-01T00:00:00Z',
  containers: [container('app', 'gcr.io/shop/checkout:1.0'), container('sidecar', 'envoy:1.30')],
  ephemeral_containers: [],
}

function container(name: string, image: string): Pod['containers'][number] {
  return {
    name,
    image,
    ready: true,
    restarts: 0,
    state: 'running',
    cpu_request_millicores: 0,
    cpu_limit_millicores: 0,
    memory_request_bytes: 0,
    memory_limit_bytes: 0,
  }
}

function result(over: Partial<DebugContainerResult> = {}): DebugContainerResult {
  return {
    pod: 'checkout-7f9',
    namespace: 'shop',
    container: 'debug-abcd1234',
    target_container: 'app',
    image: 'busybox:1.36',
    message: 'debug-abcd1234 is starting on checkout-7f9 — connecting once it is running',
    ...over,
  }
}

function podWith(...statuses: Pod['ephemeral_containers']): Pod {
  return { ...pod, ephemeral_containers: statuses }
}

beforeEach(() => {
  calls.length = 0
  podCalls.length = 0
  answer = async () => result()
  podAnswer = async () => podWith({ name: 'debug-abcd1234', running: true })
})
afterEach(cleanup)

describe('DebugContainerSheet', () => {
  it('states both irreversible facts before the button is usable', () => {
    render(<DebugContainerSheet cluster={cluster} pod={pod} onClose={() => {}} onStarted={() => {}} />)

    expect(screen.getByText(/cannot be removed/)).toBeTruthy()
    expect(screen.getByText(/shares the target container.s process namespace/)).toBeTruthy()
    expect(calls).toHaveLength(0)
    expect(podCalls).toHaveLength(0)
  })

  it('offers a container picker when the pod has more than one, defaulting to the first', () => {
    render(<DebugContainerSheet cluster={cluster} pod={pod} onClose={() => {}} onStarted={() => {}} />)

    const picker = screen.getByRole('combobox', { name: 'Container to debug' }) as HTMLSelectElement
    expect(picker.value).toBe('app')
  })

  it('writes against the chosen container, waits for it to run, then hands the new one back', async () => {
    const started = vi.fn()
    render(<DebugContainerSheet cluster={cluster} pod={pod} onClose={() => {}} onStarted={started} />)

    fireEvent.change(screen.getByRole('combobox', { name: 'Container to debug' }), {
      target: { value: 'sidecar' },
    })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Start debug session/ }))
    })

    // The write landing is not the same as the container being attachable —
    // `onStarted` fires only once a poll of the pod's own status says so, never
    // straight off the write's own response.
    await waitFor(() => expect(started).toHaveBeenCalledTimes(1))
    expect(calls).toEqual([[7, 'checkout-7f9', 'shop', 'sidecar']])
    expect(podCalls[0]).toEqual([7, 'shop', 'checkout-7f9'])
    // The exec half is told to address the container that was created, never
    // the one the debug session shared a namespace with.
    expect(started.mock.calls[0][0].container).toBe('debug-abcd1234')
  })

  it('reports plain-text progress while a just-added container has not started yet', async () => {
    // No entry for it at all yet — the status has not caught up with the write
    // that just happened, the earliest moment this sheet has to describe.
    podAnswer = async () => podWith()
    const started = vi.fn()
    render(<DebugContainerSheet cluster={cluster} pod={pod} onClose={() => {}} onStarted={started} />)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Start debug session/ }))
    })

    await waitFor(() =>
      expect(screen.getByText(/is starting on checkout-7f9 — waiting for it to report running/)).toBeTruthy(),
    )
    expect(started).not.toHaveBeenCalled()
    // No retry button while waiting — there is nothing to retry, only to watch.
    expect(screen.queryByRole('button', { name: /Start debug session/ })).toBeNull()
  })

  it('surfaces a waiting reason rather than opening a terminal that would fail', async () => {
    podAnswer = async () =>
      podWith({
        name: 'debug-abcd1234',
        running: false,
        reason: 'ErrImagePull',
        message: 'rpc error: manifest unknown',
      })
    const started = vi.fn()
    render(<DebugContainerSheet cluster={cluster} pod={pod} onClose={() => {}} onStarted={started} />)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Start debug session/ }))
    })

    await waitFor(() => expect(screen.getByText(/ErrImagePull/)).toBeTruthy())
    expect(screen.getByText(/rpc error: manifest unknown/)).toBeTruthy()
    expect(started).not.toHaveBeenCalled()
    // Nothing about a stalled start closes the sheet on its own — there is no
    // way to retry from here, only to watch or to close.
    expect(screen.queryByRole('button', { name: /Start debug session/ })).toBeNull()
  })

  it("hands back the server's refusal rather than closing", async () => {
    answer = async () => {
      throw new Error('nope')
    }
    const started = vi.fn()
    render(<DebugContainerSheet cluster={cluster} pod={pod} onClose={() => {}} onStarted={started} />)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Start debug session/ }))
    })

    await waitFor(() =>
      expect(screen.getByText('A debug container could not be added to checkout-7f9.')).toBeTruthy(),
    )
    expect(started).not.toHaveBeenCalled()
    expect(podCalls).toHaveLength(0)
  })

  it('skips the picker for a single-container pod and targets it directly', async () => {
    const single: Pod = { ...pod, containers: [pod.containers[0]] }
    const started = vi.fn()
    render(<DebugContainerSheet cluster={cluster} pod={single} onClose={() => {}} onStarted={started} />)

    expect(screen.queryByRole('combobox', { name: 'Container to debug' })).toBeNull()

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Start debug session/ }))
    })

    await waitFor(() => expect(started).toHaveBeenCalledTimes(1))
    expect(calls).toEqual([[7, 'checkout-7f9', 'shop', 'app']])
  })
})
