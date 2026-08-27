import { describe, expect, it } from 'vitest'
import type { ShellState } from '../api/types'
import { shellLifetime, shellReach, shellSecondsLeft, shellView } from './shell'

/*
 * The shell page is a state machine with a different button under every state,
 * and getting one of them wrong is the difference between "the image is still
 * pulling" and "this cluster has no agent" — two answers that look identical if
 * they both collapse into a spinner. So the derivation is asserted here rather
 * than looked at once in a browser.
 */

function state(overrides: Partial<ShellState> = {}): ShellState {
  return {
    enabled: true,
    available: true,
    idle_timeout_seconds: 3600,
    max_lifetime_seconds: 8 * 3600,
    recorded: true,
    k8s_role: 'edit',
    status: { exists: false, ready: false },
    ...overrides,
  }
}

describe('shellView', () => {
  it('separates a switched-off server from a cluster that cannot carry a shell', () => {
    const off = shellView(state({ enabled: false, reason: 'switched off' }))
    expect(off).toEqual({ kind: 'disabled', reason: 'switched off' })

    const direct = shellView(state({ available: false, reason: 'needs agent mode' }))
    expect(direct).toEqual({ kind: 'unavailable', reason: 'needs agent mode' })
  })

  it('always has something to say, even when the server sent no reason', () => {
    expect(shellView(state({ enabled: false }))?.kind).toBe('disabled')
    expect((shellView(state({ enabled: false })) as { reason: string }).reason).not.toBe('')
  })

  it('reads no pod as the ordinary state rather than as an error', () => {
    expect(shellView(state())).toEqual({ kind: 'absent' })
  })

  it('separates a pod that is coming up from one that can be typed into', () => {
    const starting = shellView(
      state({ status: { exists: true, ready: false, phase: 'Pending', message: 'ImagePullBackOff' } }),
    )
    expect(starting).toEqual({ kind: 'starting', message: 'ImagePullBackOff' })

    expect(shellView(state({ status: { exists: true, ready: true, phase: 'Running' } }))).toEqual({
      kind: 'ready',
    })
  })

  it('treats a finished pod as ended rather than as a shell to attach to', () => {
    for (const phase of ['Succeeded', 'Failed']) {
      expect(
        shellView(state({ status: { exists: true, ready: false, phase, message: 'DeadlineExceeded' } })),
      ).toEqual({ kind: 'ended', message: 'DeadlineExceeded' })
    }
  })

  it('has nothing to draw before the first read lands', () => {
    expect(shellView(undefined)).toBeNull()
  })
})

describe('the disclosure', () => {
  it('says the caller’s own grant back to them', () => {
    const scoped = shellReach(state({ k8s_role: 'view', namespaces: ['payments', 'shop'] }))
    expect(scoped).toContain('view')
    expect(scoped).toContain('payments, shop')

    // An unscoped grant is a different phrase, not an empty list.
    expect(shellReach(state({ k8s_role: 'edit' }))).toContain('every namespace')
  })

  it('states both clocks and that nothing is kept', () => {
    const said = shellLifetime(state())
    expect(said).toContain('1 hour')
    expect(said).toContain('8 hours')
    expect(said).toContain('nothing is kept')
  })
})

describe('shellSecondsLeft', () => {
  const now = Date.parse('2026-08-27T10:00:00Z')

  it('counts down to the pod’s own deadline', () => {
    const left = shellSecondsLeft(
      state({ status: { exists: true, ready: true, expires_at: '2026-08-27T12:00:00Z' } }),
      now,
    )
    expect(left).toBe(2 * 3600)
  })

  it('never runs backwards past zero', () => {
    expect(
      shellSecondsLeft(
        state({ status: { exists: true, ready: true, expires_at: '2026-08-27T09:00:00Z' } }),
        now,
      ),
    ).toBe(0)
  })

  it('has nothing to count when there is no pod or no deadline', () => {
    expect(shellSecondsLeft(state(), now)).toBeNull()
    expect(shellSecondsLeft(state({ status: { exists: true, ready: true } }), now)).toBeNull()
    expect(
      shellSecondsLeft(state({ status: { exists: true, ready: true, expires_at: 'not a date' } }), now),
    ).toBeNull()
  })
})
