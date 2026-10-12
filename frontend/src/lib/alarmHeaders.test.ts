import { describe, expect, it } from 'vitest'

import { headersHint, headersPayload } from './alarmHeaders'

describe('headersPayload', () => {
  it('omits the field for an empty box, which keeps the stored set', () => {
    expect(headersPayload('', false)).toBeUndefined()
    expect(headersPayload('   ', false)).toBeUndefined()
  })

  it('sends a typed set, trimmed, to replace the stored one', () => {
    expect(headersPayload(' {"X-Tenant":"prod"} ', false)).toBe('{"X-Tenant":"prod"}')
  })

  it('sends an empty string to clear, whatever is typed', () => {
    expect(headersPayload('{"X-Tenant":"prod"}', true)).toBe('')
  })
})

describe('headersHint', () => {
  it('names the stored headers and never a value', () => {
    const hint = headersHint(['X-Api-Key', 'X-Tenant'], false)
    expect(hint).toMatch(/X-Api-Key, X-Tenant/)
    expect(hint).toMatch(/never read back/)
  })

  it('says a clear will remove them', () => {
    expect(headersHint(['X-Tenant'], true)).toMatch(/will be removed/)
  })

  it('falls back to the general hint when nothing is stored', () => {
    expect(headersHint(undefined, false)).toMatch(/Optional JSON object/)
  })
})
