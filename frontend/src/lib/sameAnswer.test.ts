import { describe, expect, it } from 'vitest'
import { sameAnswer } from './query'

describe('sameAnswer', () => {
  it('returns true for identical references', () => {
    const obj = { items: [] }
    expect(sameAnswer(obj, obj)).toBe(true)
  })

  it('returns true for deeply equal primitives', () => {
    expect(sameAnswer(1, 1)).toBe(true)
    expect(sameAnswer('x', 'x')).toBe(true)
  })

  it('returns false for different primitives', () => {
    expect(sameAnswer(1, 2)).toBe(false)
  })

  // Array length short-circuit
  it('returns false for arrays of different length without stringifying', () => {
    const a = [1, 2, 3]
    const b = [1, 2]
    expect(sameAnswer(a, b)).toBe(false)
  })

  it('returns true for arrays with the same content', () => {
    expect(sameAnswer([1, 2], [1, 2])).toBe(true)
  })

  // Object with items array — count short-circuit
  it('returns false when items arrays have different lengths', () => {
    const a = { items: [{ name: 'pod-a' }], available: true }
    const b = { items: [], available: true }
    expect(sameAnswer(a, b)).toBe(false)
  })

  // resource_version short-circuit
  it('returns true when all items share the same resource_version', () => {
    const a = { items: [{ name: 'pod-a', resource_version: '100' }] }
    const b = { items: [{ name: 'pod-a', resource_version: '100' }] }
    expect(sameAnswer(a, b)).toBe(true)
  })

  it('returns false when any item resource_version differs', () => {
    const a = { items: [{ name: 'pod-a', resource_version: '100' }, { name: 'pod-b', resource_version: '200' }] }
    const b = { items: [{ name: 'pod-a', resource_version: '100' }, { name: 'pod-b', resource_version: '201' }] }
    expect(sameAnswer(a, b)).toBe(false)
  })

  it('falls through to stringify when items lack resource_version', () => {
    // Items without RV: must still compare correctly via stringify
    const a = { items: [{ name: 'pod-a', phase: 'Running' }] }
    const b = { items: [{ name: 'pod-a', phase: 'Running' }] }
    expect(sameAnswer(a, b)).toBe(true)
  })

  it('returns false via stringify when items content differs but no resource_version', () => {
    const a = { items: [{ name: 'pod-a', phase: 'Running' }] }
    const b = { items: [{ name: 'pod-a', phase: 'Pending' }] }
    expect(sameAnswer(a, b)).toBe(false)
  })

  it('falls through to stringify when only some items have resource_version', () => {
    const a = { items: [{ resource_version: '1' }, { name: 'no-rv' }] }
    const b = { items: [{ resource_version: '1' }, { name: 'no-rv' }] }
    // Cannot short-circuit on RV (not all have it), must still return true via stringify
    expect(sameAnswer(a, b)).toBe(true)
  })

  it('handles empty items array by falling through to stringify', () => {
    const a = { items: [], available: true }
    const b = { items: [], available: true }
    expect(sameAnswer(a, b)).toBe(true)
  })

  it('handles non-serializable values as changed', () => {
    const a = { x: BigInt(1) }
    const b = { x: BigInt(1) }
    expect(sameAnswer(a, b)).toBe(false)
  })
})
