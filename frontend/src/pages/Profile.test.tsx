/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import type { MFAStatus, User } from '../api/types'
import { profileAbilities } from '../lib/profile'
import { ProfileBody } from './Profile'

/*
 * The two acts on this page are the owner's only where the account's details
 * live here. They are absent — not disabled — otherwise, because the server
 * refuses both for a federated or machine account and a door that never opens
 * is worse than no door.
 */

function account(over: Partial<User>): User {
  return {
    id: 7,
    username: 'devops',
    role: 'user',
    system_role: 'user',
    is_active: true,
    auth_source: 'local',
    account_type: 'user',
    created_at: '2026-08-01T00:00:00Z',
    ...over,
  } as User
}

function draw(user: User, mfa?: MFAStatus | null) {
  return render(
    <MemoryRouter>
      <ProfileBody user={user} mfa={mfa} onEdit={() => {}} onChangePassword={() => {}} />
    </MemoryRouter>,
  )
}

const noFactor: MFAStatus = {
  applies: true,
  required: false,
  enrolled: false,
  factors: [],
  recovery_codes_remaining: 0,
}

afterEach(cleanup)

describe('what the owner may do with their own account', () => {
  it('lets a local account edit its details and change its password', () => {
    expect(profileAbilities(account({}))).toEqual({ editDetails: true, changePassword: true })
    draw(account({}))
    expect(screen.getByRole('button', { name: /edit profile/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: /change password/i })).toBeTruthy()
  })

  it('offers a federated account neither, and says where they live', () => {
    const user = account({ auth_source: 'oidc' })
    expect(profileAbilities(user)).toEqual({ editDetails: false, changePassword: false })
    const { container } = draw(user)
    expect(screen.queryByRole('button', { name: /edit profile/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /change password/i })).toBeNull()
    expect(container.textContent).toMatch(/identity provider/)
  })

  it('offers a machine account neither', () => {
    expect(profileAbilities(account({ account_type: 'machine' }))).toEqual({
      editDetails: false,
      changePassword: false,
    })
  })
})

describe('what the page shows', () => {
  it('names the account, its role and its email', () => {
    const { container } = draw(account({ email: 'devops@example.com', system_role: 'admin' }))
    expect(container.textContent).toContain('devops')
    expect(container.textContent).toContain('Administrator')
    expect(container.textContent).toContain('devops@example.com')
  })

  it('says an absent email is absent rather than drawing a blank', () => {
    const { container } = draw(account({}))
    expect(container.textContent).toContain('None recorded')
  })

  it('links to the access and credentials pages instead of repeating them', () => {
    draw(account({}))
    expect(screen.getByRole('link', { name: /my access/i }).getAttribute('href')).toBe('/me/access')
    expect(screen.getByRole('link', { name: /my credentials/i }).getAttribute('href')).toBe(
      '/me/credentials',
    )
  })
})

describe('the second factor', () => {
  it('offers a local account to set one up', () => {
    draw(account({}), noFactor)
    expect(screen.getByText('Two-factor authentication')).toBeTruthy()
    expect(screen.getByRole('button', { name: /set up/i })).toBeTruthy()
  })

  it('offers an LDAP account the same, because a bind is only a password', () => {
    draw(account({ auth_source: 'ldap' }), noFactor)
    expect(screen.getByRole('button', { name: /set up/i })).toBeTruthy()
  })

  it('draws no panel for an OIDC account, whose factor is its provider’s', () => {
    draw(account({ auth_source: 'oidc' }), noFactor)
    expect(screen.queryByText('Two-factor authentication')).toBeNull()
  })

  it('lists factors with a way to remove each and says how many codes are left', () => {
    draw(account({}), {
      ...noFactor,
      enrolled: true,
      required: true,
      factors: [{ id: 4, kind: 'totp', name: 'Work phone', created_at: '2026-10-01T00:00:00Z' }],
      recovery_codes_remaining: 1,
    })
    expect(screen.getByText('Work phone')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Remove' })).toBeTruthy()
    expect(screen.getByText('1 recovery code left.')).toBeTruthy()
    expect(screen.getByText(/organisation requires/)).toBeTruthy()
    expect(screen.getByRole('button', { name: /add a factor/i })).toBeTruthy()
  })
})
