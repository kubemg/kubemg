import { describe, expect, it } from 'vitest'

import type { NodeCapacity } from '../api/types'
import {
  allocationTone,
  fitShare,
  nodeStateSegments,
  qosSegments,
  relativeShare,
  segmentShares,
} from './capacityView'

describe('allocationTone', () => {
  it('mirrors the server lines and adds none', () => {
    expect(allocationTone(89.9)).toBe('ok')
    expect(allocationTone(90)).toBe('warn')
    expect(allocationTone(100)).toBe('bad')
    expect(allocationTone(140)).toBe('bad')
  })
})

describe('segmentShares', () => {
  it('drops zero segments and splits the rest', () => {
    const shares = segmentShares(qosSegments({ guaranteed: 0, burstable: 3, best_effort: 1 }))
    expect(shares.map((segment) => [segment.key, segment.share])).toEqual([
      ['burstable', 75],
      ['best_effort', 25],
    ])
  })

  it('draws nothing for nothing rather than dividing by zero', () => {
    expect(segmentShares(qosSegments({ guaranteed: 0, burstable: 0, best_effort: 0 }))).toEqual([])
  })
})

describe('nodeStateSegments', () => {
  const node = (ready: boolean, schedulable: boolean) =>
    ({ ready, schedulable }) as unknown as NodeCapacity

  it('counts a node both not ready and cordoned once, as not ready', () => {
    const values = Object.fromEntries(
      nodeStateSegments([node(true, true), node(false, false), node(true, false)]).map(
        (segment) => [segment.key, segment.value],
      ),
    )
    expect(values).toEqual({ ready: 1, cordoned: 1, not_ready: 1 })
  })
})

describe('fitShare', () => {
  it('is full when one node holds everything free', () => {
    expect(fitShare(6.67, 6.67)).toBe(100)
  })
  it('is a sliver when the free total is spread thin', () => {
    expect(fitShare(1, 12)).toBeCloseTo(8.33, 1)
  })
  it('is empty with nothing free', () => {
    expect(fitShare(0, 0)).toBe(0)
  })
})

describe('relativeShare', () => {
  it('measures each row against the largest in its list', () => {
    expect(relativeShare(5, [20, 10, 5])).toBe(25)
    expect(relativeShare(20, [20, 10, 5])).toBe(100)
    expect(relativeShare(0, [0])).toBe(0)
  })
})
