import { describe, expect, it } from 'vitest'

import { downloadLinkState } from './agentInstall'

describe('downloadLinkState', () => {
  const now = Date.parse('2026-09-24T12:00:00Z')

  it('says a live URL works once and until when', () => {
    const state = downloadLinkState('2026-09-24T12:15:00Z', now)
    expect(state.expired).toBe(false)
    expect(state.note).toMatch(/works once, until 2026-09-24 /)
    expect(state.note).toMatch(/either form spends it/)
  })

  it('says an expired URL is dead and where a fresh one comes from', () => {
    const state = downloadLinkState('2026-09-24T11:59:59Z', now)
    expect(state.expired).toBe(true)
    expect(state.note).toMatch(/expired/)
    expect(state.note).toMatch(/fresh one/)
  })

  it('still says single-use when the server sent no expiry', () => {
    const state = downloadLinkState(undefined, now)
    expect(state.expired).toBe(false)
    expect(state.note).toMatch(/works once/)
  })
})
