/**
 * @vitest-environment jsdom
 */
import { act, cleanup, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { BrowserRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import { useUrlFlag, useUrlList, useUrlText } from './urlState'

function wrapper({ children }: { children: ReactNode }) {
  return <BrowserRouter>{children}</BrowserRouter>
}

afterEach(() => {
  cleanup()
  window.history.replaceState(null, '', '/')
})

describe('filters kept in the address', () => {
  it('reads and writes a text value, dropping the key at its fallback', () => {
    window.history.replaceState(null, '', '/audit?q=payments')
    const { result } = renderHook(() => useUrlText('q'), { wrapper })
    expect(result.current[0]).toBe('payments')

    act(() => result.current[1]('checkout'))
    expect(window.location.search).toBe('?q=checkout')

    act(() => result.current[1](''))
    expect(window.location.search).toBe('')
  })

  it('composes two writes made in one handler', () => {
    window.history.replaceState(null, '', '/audit?from=a&to=b&q=x')
    const { result } = renderHook(() => [useUrlText('from'), useUrlText('to')] as const, {
      wrapper,
    })
    act(() => {
      result.current[0][1]('')
      result.current[1][1]('')
    })
    expect(window.location.search).toBe('?q=x')
  })

  it('keeps a flag and a set', () => {
    const { result } = renderHook(() => [useUrlFlag('failed'), useUrlList('verb')] as const, {
      wrapper,
    })
    act(() => result.current[0][1]((was) => !was))
    act(() => result.current[1][1]((was) => [...was, 'get', 'list']))
    expect(new URLSearchParams(window.location.search).get('failed')).toBe('true')
    expect(new URLSearchParams(window.location.search).get('verb')).toBe('get,list')
    expect(result.current[1][0]).toEqual(['get', 'list'])
  })
})
