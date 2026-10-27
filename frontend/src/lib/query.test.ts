// @vitest-environment jsdom
import { act, renderHook } from '@testing-library/react'
import { AxiosError } from 'axios'
import type { AxiosResponse } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { LIVE_INTERVAL } from './live'
import { invalidateQueries, useCachedQuery } from './query'

/*
 * A live tick never re-asks a refused question. KubeMG records its own
 * refusals in the audit trail, so a page that cannot be answered and kept
 * asking would write a row every fifteen seconds for every viewer.
 */

function answered(status: number): AxiosError {
  return new AxiosError('answered', 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: '',
    data: { error: 'no' },
    headers: {},
    config: {},
  } as AxiosResponse)
}

async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function tick(times = 1) {
  for (let i = 0; i < times; i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_INTERVAL)
    })
  }
}

describe('useCachedQuery live ticks', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    invalidateQueries()
  })

  it('does not re-ask a refused read on the tick', async () => {
    const read = vi.fn().mockRejectedValue(answered(403))
    const { result } = renderHook(() => useCachedQuery('refused', read, { live: true }))
    await settle()
    expect(read).toHaveBeenCalledTimes(1)
    expect(result.current.error).toBeTruthy()

    await tick(3)
    expect(read).toHaveBeenCalledTimes(1)
    expect(result.current.error).toBeTruthy()
  })

  it('still retries a failure on the tick', async () => {
    const read = vi.fn().mockRejectedValue(answered(503))
    renderHook(() => useCachedQuery('failed', read, { live: true }))
    await settle()
    expect(read).toHaveBeenCalledTimes(1)

    await tick(2)
    expect(read).toHaveBeenCalledTimes(3)
  })

  it('asks again when the question changes', async () => {
    const read = vi.fn().mockRejectedValue(answered(404))
    const { rerender } = renderHook(({ key }) => useCachedQuery(key, read, { live: true }), {
      initialProps: { key: 'first' },
    })
    await settle()
    await tick()
    expect(read).toHaveBeenCalledTimes(1)

    rerender({ key: 'second' })
    await settle()
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('asks again on Refresh, and ticks again once answered', async () => {
    const read = vi
      .fn()
      .mockRejectedValueOnce(answered(403))
      .mockResolvedValue({ items: [] })
    const { result } = renderHook(() => useCachedQuery('refreshed', read, { live: true }))
    await settle()
    await tick()
    expect(read).toHaveBeenCalledTimes(1)

    await act(async () => {
      await result.current.refresh()
    })
    expect(read).toHaveBeenCalledTimes(2)
    expect(result.current.data).toEqual({ items: [] })

    await tick()
    expect(read).toHaveBeenCalledTimes(3)
  })
})
