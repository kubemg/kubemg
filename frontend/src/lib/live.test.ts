import { AxiosError } from 'axios'
import type { AxiosResponse } from 'axios'
import { describe, expect, it } from 'vitest'

import { isRefusal } from './live'

function answered(status: number): AxiosError {
  return new AxiosError('answered', 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: '',
    data: {},
    headers: {},
    config: {},
  } as AxiosResponse)
}

describe('isRefusal', () => {
  it('reads a 4xx as the same answer next time', () => {
    for (const status of [400, 401, 403, 404, 409, 410]) {
      expect(isRefusal(answered(status)), String(status)).toBe(true)
    }
  })

  it('keeps "not now" retryable', () => {
    expect(isRefusal(answered(408))).toBe(false)
    expect(isRefusal(answered(429))).toBe(false)
  })

  it('keeps a failure retryable', () => {
    for (const status of [500, 502, 503, 504]) {
      expect(isRefusal(answered(status)), String(status)).toBe(false)
    }
    expect(isRefusal(new AxiosError('Network Error', 'ERR_NETWORK'))).toBe(false)
    expect(isRefusal(new Error('boom'))).toBe(false)
    expect(isRefusal(null)).toBe(false)
  })
})
