/**
 * @vitest-environment jsdom
 */
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { useDisclosureState } from './disclosures'

/*
 * The four acceptance rules the issue named, each as its own case: closed on
 * a first visit, remembered whichever way it was last left, storage that
 * cannot be trusted reading as closed rather than throwing, and one person's
 * choice never leaking into another's.
 */

afterEach(() => {
  window.localStorage.clear()
})

describe('useDisclosureState', () => {
  it('starts closed on a first visit', () => {
    const { result } = renderHook(() => useDisclosureState('card', 1))
    expect(result.current[0]).toBe(false)
  })

  it('remembers an opened card on the next visit', () => {
    const { result } = renderHook(() => useDisclosureState('card', 1))
    act(() => result.current[1](true))

    // A fresh mount is the next visit: it reads what was stored rather than
    // starting closed again.
    const { result: nextVisit } = renderHook(() => useDisclosureState('card', 1))
    expect(nextVisit.current[0]).toBe(true)
  })

  it('remembers a card closed again after it was opened', () => {
    const { result } = renderHook(() => useDisclosureState('card', 1))
    act(() => result.current[1](true))
    act(() => result.current[1](false))

    const { result: nextVisit } = renderHook(() => useDisclosureState('card', 1))
    expect(nextVisit.current[0]).toBe(false)
  })

  it('reads a garbage stored value as closed rather than trusting it', () => {
    window.localStorage.setItem('kubemg.disclosures.1.card', 'not-a-real-value')
    const { result } = renderHook(() => useDisclosureState('card', 1))
    expect(result.current[0]).toBe(false)
  })

  it('reads as closed and never throws when storage refuses to answer', () => {
    const original = window.localStorage.getItem
    window.localStorage.getItem = () => {
      throw new Error('storage unavailable')
    }
    try {
      const { result } = renderHook(() => useDisclosureState('card', 1))
      expect(result.current[0]).toBe(false)
      expect(() => act(() => result.current[1](true))).not.toThrow()
    } finally {
      window.localStorage.getItem = original
    }
  })

  it("keeps one signed-in identity's choice apart from another's", () => {
    const { result: userA } = renderHook(() => useDisclosureState('card', 1))
    act(() => userA.current[1](true))

    const { result: userB } = renderHook(() => useDisclosureState('card', 2))
    expect(userB.current[0]).toBe(false)
  })

  it('never mixes up two different cards for the same identity', () => {
    const { result: cardA } = renderHook(() => useDisclosureState('card-a', 1))
    act(() => cardA.current[1](true))

    const { result: cardB } = renderHook(() => useDisclosureState('card-b', 1))
    expect(cardB.current[0]).toBe(false)
  })
})
