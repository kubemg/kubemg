/**
 * @vitest-environment jsdom
 */
import { cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const audit = vi.fn()
const expiring = vi.fn()

vi.mock('../api/client', () => ({
  fetchAudit: (...args: unknown[]) => audit(...args),
  countExpiringKubeconfigs: (...args: unknown[]) => expiring(...args),
}))

import { useFleetCounts } from './fleetCounts'

afterEach(cleanup)
beforeEach(() => {
  audit.mockReset()
  expiring.mockReset()
})

describe('useFleetCounts', () => {
  it('reads each count once, with the same filter its figure links to', async () => {
    audit.mockResolvedValue({ total: 14 })
    expiring.mockResolvedValue(3)

    const { result, rerender } = renderHook(({ admin }) => useFleetCounts(admin), {
      initialProps: { admin: true },
    })
    expect(result.current).toEqual({ refused: undefined, expiring: undefined })
    await waitFor(() => expect(result.current).toEqual({ refused: 14, expiring: 3 }))

    expect(audit).toHaveBeenCalledWith({ failed: true, range: '24h', limit: 1 })
    expect(expiring).toHaveBeenCalledWith('24h')

    // A re-render — which is all a live tick of the fleet list is to this hook —
    // reads nothing again.
    rerender({ admin: true })
    rerender({ admin: true })
    expect(audit).toHaveBeenCalledTimes(1)
    expect(expiring).toHaveBeenCalledTimes(1)
  })

  it('turns a failed read into unknown for that figure alone', async () => {
    audit.mockRejectedValue(new Error('trail unavailable'))
    expiring.mockResolvedValue(0)

    const { result } = renderHook(() => useFleetCounts(true))
    await waitFor(() => expect(result.current).toEqual({ refused: null, expiring: 0 }))
  })

  it('never reads the trail for a non-admin', async () => {
    expiring.mockResolvedValue(1)

    const { result } = renderHook(() => useFleetCounts(false))
    await waitFor(() => expect(result.current.expiring).toBe(1))
    expect(audit).not.toHaveBeenCalled()
    expect(result.current.refused).toBeUndefined()
  })
})
