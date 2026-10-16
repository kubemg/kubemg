/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it } from 'vitest'

import { forgetReturnTo, isSafeReturnPath, rememberReturnTo, returnTo } from './returnTo'

afterEach(() => forgetReturnTo())

describe('where a sign-in sends somebody back to', () => {
  it('keeps the page and its query, so a CLI code survives the sign-in', () => {
    rememberReturnTo('/cli/activate?code=BCDF-GHJK')
    expect(returnTo()).toBe('/cli/activate?code=BCDF-GHJK')
    forgetReturnTo()
    expect(returnTo()).toBe('/')
  })

  it('never leaves the console', () => {
    for (const path of ['https://evil.example', '//evil.example/x', '/\\evil.example', 'javascript:alert(1)', '/login']) {
      expect(isSafeReturnPath(path)).toBe(false)
      rememberReturnTo(path)
      expect(returnTo()).toBe('/')
    }
  })
})
