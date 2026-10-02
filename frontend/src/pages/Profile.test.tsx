/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import type { User } from '../api/types'
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

function draw(user: User) {
  return render(
    <MemoryRouter>
      <ProfileBody user={user} onEdit={() => {}} onChangePassword={() => {}} />
    </MemoryRouter>,
  )
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
