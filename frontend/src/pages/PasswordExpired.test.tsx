/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { User } from '../api/types'
import { AuthContext } from '../state/auth-context'
import type { AuthState } from '../state/auth-context'
import { PasswordExpiredBody } from './PasswordExpired'

const api = vi.hoisted(() => ({
  changeOwnPassword: vi.fn(),
  fetchMe: vi.fn(),
}))

vi.mock('../api/client', () => ({
  changeOwnPassword: api.changeOwnPassword,
  fetchMe: api.fetchMe,
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

const due: User = {
  id: 2,
  username: 'devops',
  role: 'user',
  system_role: 'user',
  is_active: true,
  auth_source: 'local',
  account_type: 'user',
  can_view_recordings: false,
  can_reveal_secrets: false,
  created_at: '2026-01-01T00:00:00Z',
  password_expires_at: '2026-10-01T00:00:00Z',
  password_change_required: true,
}

function draw(replaceUser = vi.fn(), signOut = vi.fn()) {
  const auth: AuthState = {
    user: due,
    loading: false,
    signIn: async () => null,
    adoptSession: async () => {},
    signOut,
    replaceUser,
    setupRequired: false,
    setupLoading: false,
    refreshSetupState: async () => {},
  }
  render(
    <AuthContext.Provider value={auth}>
      <PasswordExpiredBody user={due} onSignOut={signOut} />
    </AuthContext.Provider>,
  )
  return { replaceUser, signOut }
}

function type(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

async function submit() {
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: /change password and continue/i }))
  })
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('PasswordExpiredBody', () => {
  it('asks for the current password, the new one, and the new one again', () => {
    draw()
    expect(screen.getByLabelText('Current password')).toBeTruthy()
    expect(screen.getByLabelText('New password')).toBeTruthy()
    expect(screen.getByLabelText('Confirm the new password')).toBeTruthy()
    expect(screen.getByText('devops')).toBeTruthy()
  })

  it('sends nothing while the two new passwords differ', async () => {
    draw()
    type('Current password', 'old-password')
    type('New password', 'new-password')
    type('Confirm the new password', 'new-passwrod')
    await submit()
    expect(api.changeOwnPassword).not.toHaveBeenCalled()
    expect(screen.getByText('The two do not match.')).toBeTruthy()
  })

  it('changes the password and adopts the account read back', async () => {
    const cleared = { ...due, password_change_required: false, password_expires_at: undefined }
    api.changeOwnPassword.mockResolvedValue({ changed: true })
    api.fetchMe.mockResolvedValue(cleared)
    const { replaceUser } = draw()
    type('Current password', 'old-password')
    type('New password', 'new-password')
    type('Confirm the new password', 'new-password')
    await submit()
    expect(api.changeOwnPassword).toHaveBeenCalledWith({
      current_password: 'old-password',
      new_password: 'new-password',
    })
    expect(replaceUser).toHaveBeenCalledWith(cleared)
  })

  it('stays on the screen when the change is refused', async () => {
    api.changeOwnPassword.mockRejectedValue(new Error('401'))
    const { replaceUser } = draw()
    type('Current password', 'wrong')
    type('New password', 'new-password')
    type('Confirm the new password', 'new-password')
    await submit()
    expect(replaceUser).not.toHaveBeenCalled()
    expect(screen.getByText('Could not change the password.')).toBeTruthy()
  })

  it('offers a way out', () => {
    const { signOut } = draw()
    fireEvent.click(screen.getByRole('button', { name: /sign out instead/i }))
    expect(signOut).toHaveBeenCalled()
  })
})
