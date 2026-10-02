/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SSOProviderSummary } from '../api/types'
import { SIGN_IN_TEXT } from '../lib/signInError'
import { AuthContext } from '../state/auth-context'
import type { AuthState } from '../state/auth-context'
import { BrandingContext } from '../state/branding-context'
import { Login } from './Login'

/*
 * The sign-in card is the same size on every install. The provider slot holds
 * one provider's height with none configured, while the read is in flight and
 * with one, so configuring SSO fills a place rather than growing the card.
 * jsdom has no layout, so this is asserted on the structure that guarantees it:
 * the slot is present, last, and carries the same classes in every state.
 */

let answer: () => Promise<SSOProviderSummary[]> = () => Promise.resolve([])

vi.mock('../api/client', () => ({
  fetchSSOProviders: () => answer(),
  ssoLoginURL: (id: number) => `/api/v1/auth/sso/providers/${id}/login`,
  ssoPasswordLogin: () => Promise.reject(new Error('not used here')),
}))

const oidc: SSOProviderSummary = { id: 3, name: 'Okta', protocol: 'oidc', interactive: true }
const ldap: SSOProviderSummary = { id: 4, name: 'Corp directory', protocol: 'ldap', interactive: false }

function auth(signIn: AuthState['signIn'] = async () => {}): AuthState {
  return {
    user: null,
    loading: false,
    signIn,
    adoptSession: async () => {},
    signOut: () => {},
    replaceUser: () => {},
    setupRequired: false,
    setupLoading: false,
    refreshSetupState: async () => {},
  }
}

async function draw(signIn?: AuthState['signIn']) {
  const view = render(
    <BrandingContext.Provider value={{ branding: {}, refresh: async () => {} }}>
      <AuthContext.Provider value={auth(signIn)}>
        <Login />
      </AuthContext.Provider>
    </BrandingContext.Provider>,
  )
  // Let the provider read answer (or not) before anything is asserted.
  await act(async () => {})
  return view
}

function slotShape() {
  const card = screen.getByTestId('sign-in-card')
  const slot = card.querySelector<HTMLElement>('[data-slot="sso-providers"]')
  if (!slot) throw new Error('the provider slot is missing')
  return {
    slot,
    shape: {
      index: Array.from(card.children).indexOf(slot),
      siblings: card.children.length,
      last: card.lastElementChild === slot,
      className: slot.className,
    },
  }
}

function refused(status: number, error: string) {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('Request failed', 'ERR_BAD_REQUEST', config, {}, {
    status,
    statusText: '',
    headers: {},
    config,
    data: { error },
  })
}

afterEach(() => {
  cleanup()
  answer = () => Promise.resolve([])
})

describe('Login provider slot', () => {
  it('holds the same place with no provider, one provider, and while the read is in flight', async () => {
    answer = () => Promise.resolve([])
    await draw()
    const none = slotShape()
    cleanup()

    answer = () => Promise.resolve([oidc])
    await draw()
    const one = slotShape()
    cleanup()

    answer = () => new Promise(() => {})
    await draw()
    const pending = slotShape()

    expect(none.shape.last).toBe(true)
    expect(one.shape).toEqual(none.shape)
    expect(pending.shape).toEqual(none.shape)
  })

  it('draws nothing in the slot when no provider is configured', async () => {
    answer = () => Promise.resolve([])
    await draw()
    const { slot } = slotShape()
    expect(slot.children.length).toBe(0)
    expect(slot.textContent).toBe('')
    expect(screen.queryByText(/continue with/i)).toBeNull()
    expect(screen.queryByText(/single sign-on/i)).toBeNull()
  })

  it('renders each configured provider as the thing that starts its flow', async () => {
    answer = () => Promise.resolve([oidc, ldap])
    await draw()

    // An interactive provider is a navigation to its own login route.
    const link = screen.getByRole('link', { name: /Okta/ })
    expect(link.getAttribute('href')).toBe('/api/v1/auth/sso/providers/3/login')

    // A directory has no redirect: its button opens its form in place.
    const directory = screen.getByRole('button', { name: /Corp directory/ })
    expect(directory.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(directory)
    expect(directory.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByLabelText('Directory username')).toBeTruthy()
  })
})

describe('Login refusals', () => {
  async function submitRefused(error: AxiosError) {
    await draw(() => Promise.reject(error))
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'someone' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'wrong' } })
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('Username').closest('form') as HTMLFormElement)
    })
  }

  it('says a wrong password in the console voice, never the server string', async () => {
    answer = () => Promise.resolve([])
    await submitRefused(refused(401, 'invalid credentials'))
    expect(screen.getByText(SIGN_IN_TEXT.credentials)).toBeTruthy()
    expect(screen.queryByText(/invalid credentials/i)).toBeNull()
  })

  it('points at the provider when one is configured', async () => {
    answer = () => Promise.resolve([oidc])
    await submitRefused(refused(401, 'invalid credentials'))
    expect(
      screen.getByText(`${SIGN_IN_TEXT.credentials} ${SIGN_IN_TEXT.credentialsProviderHint}`),
    ).toBeTruthy()
  })

  it('reads a disabled account as itself', async () => {
    answer = () => Promise.resolve([])
    await submitRefused(refused(403, 'this account is disabled'))
    expect(screen.getByText(SIGN_IN_TEXT.disabled)).toBeTruthy()
    expect(screen.queryByText('this account is disabled')).toBeNull()
  })
})
