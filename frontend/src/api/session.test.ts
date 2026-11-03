/**
 * @vitest-environment jsdom
 */
import { AxiosError, AxiosHeaders } from 'axios'
import { describe, expect, it } from 'vitest'

import { isSessionInvalid } from './client'

function failure(status: number, data: unknown): AxiosError {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('failed', 'ERR_BAD_REQUEST', config, null, {
    status,
    statusText: '',
    headers: {},
    config,
    data,
  })
}

// Only the 401 that says the session is gone signs out. A wrong current
// password, a wrong code for a factor change, or a cluster's own refusal is an
// answer about one request and must leave the session alone.
describe('which 401 signs out', () => {
  it('signs out on the session gate', () => {
    expect(isSessionInvalid(failure(401, { error: 'invalid or expired token', code: 'session_invalid' }))).toBe(true)
  })

  it('keeps the session for a 401 about one request', () => {
    expect(isSessionInvalid(failure(401, { error: 'the current password is not right' }))).toBe(false)
    expect(isSessionInvalid(failure(401, { error: 'that code is not right' }))).toBe(false)
    expect(isSessionInvalid(failure(401, { kind: 'Status', message: 'Unauthorized' }))).toBe(false)
  })

  it('reads the code only on a 401', () => {
    expect(isSessionInvalid(failure(403, { code: 'session_invalid' }))).toBe(false)
    expect(isSessionInvalid(new Error('offline'))).toBe(false)
  })
})
