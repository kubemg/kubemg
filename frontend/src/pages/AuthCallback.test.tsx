/**
 * @vitest-environment jsdom
 */
import { act, cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ssoBinding } from '../lib/ssoBinding'
import { AuthContext } from '../state/auth-context'
import type { AuthState } from '../state/auth-context'
import { AuthCallback } from './AuthCallback'

/*
 * The callback takes a session only from a sign-in this tab started. A callback
 * URL somebody else started and sent on, or a bare #token= link, would sign the
 * person who opens it into the sender's account.
 */

function auth(adoptSession: AuthState['adoptSession']): AuthState {
  return {
    user: null,
    loading: false,
    signIn: async () => null,
    adoptSession,
    signOut: () => {},
    replaceUser: () => {},
    setupRequired: false,
    setupLoading: false,
    refreshSetupState: async () => {},
  }
}

async function land(fragment: string) {
  window.history.replaceState(null, '', `/auth/callback#${fragment}`)
  const adoptSession = vi.fn(async () => {})
  render(
    <MemoryRouter>
      <AuthContext.Provider value={auth(adoptSession)}>
        <AuthCallback />
      </AuthContext.Provider>
    </MemoryRouter>,
  )
  await act(async () => {})
  return adoptSession
}

describe('the SSO callback', () => {
  beforeEach(() => sessionStorage.clear())
  afterEach(() => cleanup())

  it('takes the session from a sign-in this tab started', async () => {
    const binding = ssoBinding()
    const adopt = await land(`token=abc&expires_at=x&binding=${binding}`)
    expect(adopt).toHaveBeenCalledWith('abc')
    expect(window.location.hash).toBe('')
  })

  it('refuses a session this tab did not ask for', async () => {
    ssoBinding()
    const adopt = await land('token=attacker&expires_at=x&binding=someone-elses-binding-value')
    expect(adopt).not.toHaveBeenCalled()
    expect(screen.getByText(/not started in this browser tab/)).toBeTruthy()
  })

  it('refuses a bare token link', async () => {
    const adopt = await land('token=attacker&expires_at=x')
    expect(adopt).not.toHaveBeenCalled()
    expect(window.location.hash).toBe('')
  })
})
