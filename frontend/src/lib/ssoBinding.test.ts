/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it } from 'vitest'
import { ssoBinding, takeSSOBinding } from './ssoBinding'

describe('ssoBinding', () => {
  beforeEach(() => sessionStorage.clear())

  it('is one token per tab until it is spent', () => {
    const first = ssoBinding()
    expect(first).toMatch(/^[A-Za-z0-9_-]{16,128}$/)
    expect(ssoBinding()).toBe(first)
  })

  it('accepts only the value this tab holds, once', () => {
    const binding = ssoBinding()
    expect(takeSSOBinding(binding)).toBe(true)
    // Spent: the same callback opened again is refused, and the next sign-in
    // draws a new value.
    expect(takeSSOBinding(binding)).toBe(false)
    expect(ssoBinding()).not.toBe(binding)
  })

  it('refuses a callback this tab did not start', () => {
    ssoBinding()
    expect(takeSSOBinding('someone-elses-binding-value')).toBe(false)
  })

  it('refuses a callback with no binding, and one in a tab that holds none', () => {
    ssoBinding()
    expect(takeSSOBinding(null)).toBe(false)
    expect(takeSSOBinding('anything-at-all-123456')).toBe(false)
    expect(takeSSOBinding(null)).toBe(false)
  })
})
