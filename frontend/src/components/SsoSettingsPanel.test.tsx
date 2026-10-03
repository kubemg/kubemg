/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SSOProvider, SSOProviderInput } from '../api/types'
import { SsoSettingsPanel } from './SsoSettingsPanel'

// Okta is OIDC or SAML underneath; what the form owes it is that choosing
// Okta reaches the server as Okta, and that a saved one says so on its card.

let providers: SSOProvider[] = []
const created: SSOProviderInput[] = []

vi.mock('../api/client', () => ({
  fetchSSOAdminProviders: () => Promise.resolve({ providers, console_origins: [] }),
  createSSOProvider: (input: SSOProviderInput) => {
    created.push(input)
    return Promise.resolve({})
  },
  updateSSOProvider: () => Promise.resolve({}),
  deleteSSOProvider: () => Promise.resolve(),
  checkSSOProvider: () => Promise.resolve({}),
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))
vi.mock('../state/confirm-context', () => ({ useConfirm: () => async () => true }))
vi.mock('../state/result-context', () => ({ useResult: () => () => {} }))

afterEach(() => {
  cleanup()
  providers = []
  created.length = 0
})

describe('SsoSettingsPanel', () => {
  it('sends an Okta provider as Okta over the protocol chosen', async () => {
    render(<SsoSettingsPanel />)
    fireEvent.click(await screen.findByRole('button', { name: /Add provider/ }))

    fireEvent.change(screen.getByLabelText('Type'), { target: { value: 'okta-oidc' } })
    expect(screen.getByText(/OIDC — Web Application/)).toBeTruthy()

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Okta' } })
    const issuer = screen.getByLabelText('Issuer URL')
    expect(issuer.getAttribute('placeholder')).toContain('okta.com')
    fireEvent.change(issuer, { target: { value: 'https://acme.okta.com/oauth2/default' } })
    fireEvent.change(screen.getByLabelText('Client ID'), { target: { value: '0oa1' } })
    // A custom authorization server is not asked for a groups scope.
    expect(screen.getByText(/Leave empty for profile email:/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Save provider' }))
    await waitFor(() => expect(created).toHaveLength(1))
    expect(created[0]).toMatchObject({ protocol: 'oidc', vendor: 'okta', issuer_url: 'https://acme.okta.com/oauth2/default' })
  })

  it('names a saved Okta provider on its card, and a generic one by protocol', async () => {
    const base = {
      enabled: true,
      ldap_use_tls: true,
      ldap_start_tls: false,
      ldap_skip_verify: false,
      allow_jit: true,
      default_system_role: 'user',
      last_status: 'pending',
      has_client_secret: false,
      has_bind_password: false,
      redirect_url: 'https://kubemg.example.com/cb',
    } as const
    providers = [
      { ...base, id: 1, name: 'Corp Okta', protocol: 'saml', vendor: 'okta' },
      { ...base, id: 2, name: 'Keycloak', protocol: 'oidc' },
    ]
    render(<SsoSettingsPanel />)
    expect(await screen.findByText('Okta — SAML 2.0')).toBeTruthy()
    expect(screen.getByText('OpenID Connect')).toBeTruthy()
  })
})
