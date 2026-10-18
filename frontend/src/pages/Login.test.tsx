/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { MFAChallenge, MFASessionResponse, SSOProviderSummary, User } from '../api/types'
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

const mfa = vi.hoisted(() => ({
  verifyMFA: vi.fn(),
  enrollMFA: vi.fn(),
  confirmMFA: vi.fn(),
  beginKeyCheck: vi.fn(),
}))

vi.mock('../api/client', () => ({
  fetchSSOProviders: () => answer(),
  ssoLoginURL: (id: number) => `/api/v1/auth/sso/providers/${id}/login`,
  ssoPasswordLogin: () => Promise.reject(new Error('not used here')),
  errorMessage: (_err: unknown, fallback: string) => fallback,
  verifyMFA: mfa.verifyMFA,
  enrollMFA: mfa.enrollMFA,
  confirmMFA: mfa.confirmMFA,
  beginKeyCheck: mfa.beginKeyCheck,
}))

const oidc: SSOProviderSummary = { id: 3, name: 'Okta', protocol: 'oidc', interactive: true }
const ldap: SSOProviderSummary = { id: 4, name: 'Corp directory', protocol: 'ldap', interactive: false }

function auth(
  signIn: AuthState['signIn'] = async () => null,
  adoptSession: AuthState['adoptSession'] = async () => {},
): AuthState {
  return {
    user: null,
    loading: false,
    signIn,
    adoptSession,
    signOut: () => {},
    replaceUser: () => {},
    setupRequired: false,
    setupLoading: false,
    refreshSetupState: async () => {},
  }
}

async function draw(signIn?: AuthState['signIn'], adoptSession?: AuthState['adoptSession']) {
  const view = render(
    <BrandingContext.Provider value={{ branding: {}, refresh: async () => {} }}>
      <AuthContext.Provider value={auth(signIn, adoptSession)}>
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
  vi.clearAllMocks()
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

describe('Login second factor', () => {
  const user = { id: 1, username: 'dev' } as User
  const session: MFASessionResponse = { token: 'session-token', expires_at: '2026-10-16T12:00:00Z', user }

  async function passwordStep(challenge: MFAChallenge, adoptSession = vi.fn(async () => {})) {
    await draw(async () => challenge, adoptSession)
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'dev' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pw' } })
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('Username').closest('form') as HTMLFormElement)
    })
    return adoptSession
  }

  it('turns the card into the code step and opens the session only after the code', async () => {
    mfa.verifyMFA.mockResolvedValue(session)
    const adopt = await passwordStep({
      mfa_required: true,
      challenge: 'kmgc_abc',
      challenge_expires_at: '2026-10-16T12:05:00Z',
      methods: ['totp'],
    })
    expect(screen.getByText('Two-factor check')).toBeTruthy()
    expect(screen.queryByLabelText('Password')).toBeNull()
    expect(adopt).not.toHaveBeenCalled()
    // No recovery codes left means no door to them.
    expect(screen.queryByRole('button', { name: 'Use a recovery code' })).toBeNull()

    fireEvent.change(screen.getByLabelText('Code'), { target: { value: '123 456' } })
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('Code').closest('form') as HTMLFormElement)
    })
    expect(mfa.verifyMFA).toHaveBeenCalledWith('kmgc_abc', '123 456')
    expect(adopt).toHaveBeenCalledWith('session-token', user)
  })

  it('offers a recovery code when the account has some, and says how many are left', async () => {
    mfa.verifyMFA.mockResolvedValue({ ...session, recovery_codes_remaining: 2 })
    const adopt = await passwordStep({
      mfa_required: true,
      challenge: 'kmgc_abc',
      challenge_expires_at: '2026-10-16T12:05:00Z',
      methods: ['totp', 'recovery'],
    })
    fireEvent.click(screen.getByRole('button', { name: 'Use a recovery code' }))
    fireEvent.change(screen.getByLabelText('Recovery code'), { target: { value: 'abcde-fghjk' } })
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('Recovery code').closest('form') as HTMLFormElement)
    })
    expect(screen.getByText(/You have 2 left/)).toBeTruthy()
    expect(adopt).not.toHaveBeenCalled()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Continue/ }))
    })
    expect(adopt).toHaveBeenCalledWith('session-token', user)
  })

  it('walks an account that must enrol through the QR code and its recovery codes before the session', async () => {
    mfa.enrollMFA.mockResolvedValue({
      factor_id: 9,
      secret: 'JBSWY3DPEHPK3PXP',
      otpauth_uri: 'otpauth://totp/KubeMG:dev',
      qr: 'data:image/png;base64,AAAA',
    })
    mfa.confirmMFA.mockResolvedValue({ recovery_codes: ['abcde-fghjk', 'mnpqr-stuvw'], session })
    const adopt = await passwordStep({
      mfa_enrollment_required: true,
      challenge: 'kmgc_enrol',
      challenge_expires_at: '2026-10-16T12:05:00Z',
    })
    await act(async () => {})
    expect(mfa.enrollMFA).toHaveBeenCalledWith(expect.objectContaining({ challenge: 'kmgc_enrol' }))
    expect(screen.getByAltText(/QR code/).getAttribute('src')).toBe('data:image/png;base64,AAAA')

    fireEvent.change(screen.getByLabelText('Code from the app'), { target: { value: '123456' } })
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('Code from the app').closest('form') as HTMLFormElement)
    })
    expect(mfa.confirmMFA).toHaveBeenCalledWith({ factor_id: 9, code: '123456', challenge: 'kmgc_enrol' })
    expect(screen.getByText(/abcde-fghjk/)).toBeTruthy()

    // The codes must be acknowledged before the console opens.
    const go = screen.getByRole('button', { name: 'Continue to kubemg' }) as HTMLButtonElement
    expect(go.disabled).toBe(true)
    fireEvent.click(screen.getByLabelText('I have saved these codes'))
    expect(go.disabled).toBe(false)
    await act(async () => {
      fireEvent.click(go)
    })
    expect(adopt).toHaveBeenCalledWith('session-token', user)
  })
})

/*
 * A security key, with the browser's half mocked: `navigator.credentials` is
 * what jsdom does not have, and the server's half is tested in Go with a
 * virtual authenticator. What is asserted here is the wiring — the ceremony is
 * opened with the sign-in's challenge, the browser is handed bytes, and what it
 * returns goes back as the proof — and that a browser without WebAuthn is never
 * offered a key it cannot use.
 */
describe('Login with a security key', () => {
  const user = { id: 1, username: 'dev' } as User
  const session: MFASessionResponse = { token: 'session-token', expires_at: '2026-10-16T12:00:00Z', user }
  const buf = (...values: number[]) => new Uint8Array(values).buffer
  const browser = { get: vi.fn(), create: vi.fn() }

  function withWebAuthn() {
    Object.defineProperty(window, 'PublicKeyCredential', { value: function PublicKeyCredential() {}, configurable: true })
    Object.defineProperty(navigator, 'credentials', { value: browser, configurable: true })
  }

  afterEach(() => {
    Reflect.deleteProperty(window, 'PublicKeyCredential')
    Reflect.deleteProperty(navigator, 'credentials')
    browser.get.mockReset()
    browser.create.mockReset()
    vi.clearAllMocks()
  })

  async function passwordStep(challenge: MFAChallenge, adoptSession = vi.fn(async () => {})) {
    await draw(async () => challenge, adoptSession)
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'dev' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pw' } })
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('Username').closest('form') as HTMLFormElement)
    })
    return adoptSession
  }

  const verify: MFAChallenge = {
    mfa_required: true,
    challenge: 'kmgc_abc',
    challenge_expires_at: '2026-10-16T12:05:00Z',
    methods: ['webauthn', 'totp', 'recovery'],
  }

  it('offers the key first, and signs in with what it returns', async () => {
    withWebAuthn()
    mfa.beginKeyCheck.mockResolvedValue({
      ceremony: 'kmgc_ceremony',
      expires_at: '2026-10-16T12:02:00Z',
      options: {
        publicKey: { challenge: 'AQID', rpId: 'kubemg.example.com', allowCredentials: [{ type: 'public-key', id: 'BAUG' }] },
      },
    })
    browser.get.mockResolvedValue({
      id: 'BAUG',
      rawId: buf(4, 5, 6),
      type: 'public-key',
      response: { clientDataJSON: buf(1), authenticatorData: buf(2), signature: buf(3), userHandle: null },
    })
    mfa.verifyMFA.mockResolvedValue(session)
    const adopt = await passwordStep(verify)

    expect(screen.queryByLabelText('Code')).toBeNull()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Use security key/ }))
    })
    expect(mfa.beginKeyCheck).toHaveBeenCalledWith('kmgc_abc')
    const asked = browser.get.mock.calls[0][0].publicKey
    expect(new Uint8Array(asked.challenge)).toEqual(new Uint8Array([1, 2, 3]))
    expect(new Uint8Array(asked.allowCredentials[0].id)).toEqual(new Uint8Array([4, 5, 6]))
    expect(mfa.verifyMFA).toHaveBeenCalledWith('kmgc_abc', '', {
      ceremony: 'kmgc_ceremony',
      credential: expect.objectContaining({
        rawId: 'BAUG',
        response: { clientDataJSON: 'AQ', authenticatorData: 'Ag', signature: 'Aw' },
      }),
    })
    expect(adopt).toHaveBeenCalledWith('session-token', user)
  })

  it('says a dismissed prompt in words, opens nothing, and still offers the app', async () => {
    withWebAuthn()
    mfa.beginKeyCheck.mockResolvedValue({ ceremony: 'kmgc_c', expires_at: '', options: { publicKey: { challenge: 'AQID' } } })
    browser.get.mockRejectedValue(new DOMException('cancelled', 'NotAllowedError'))
    const adopt = await passwordStep(verify)
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Use security key/ }))
    })
    expect(screen.getByText(/closed or timed out/)).toBeTruthy()
    expect(adopt).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Use the authenticator app' }))
    expect(screen.getByLabelText('Code')).toBeTruthy()
  })

  it('says so when the only factor is a key and this browser cannot use one', async () => {
    await passwordStep({ ...verify, methods: ['webauthn'] })
    expect(screen.getByText(/this browser cannot use one/)).toBeTruthy()
    expect(screen.queryByLabelText('Code')).toBeNull()
  })

  it('never offers a key to a browser that cannot ask one', async () => {
    await passwordStep(verify)
    expect(screen.queryByRole('button', { name: /security key/ })).toBeNull()
    expect(screen.getByLabelText('Code')).toBeTruthy()
  })

  it('lets an account that must enrol choose a key, and finishes with its recovery codes', async () => {
    withWebAuthn()
    mfa.enrollMFA.mockResolvedValue({
      factor_id: 12,
      kind: 'webauthn',
      webauthn: {
        publicKey: {
          rp: { id: 'kubemg.example.com', name: 'KubeMG' },
          user: { id: 'AAAAAAAAAAE', name: 'dev', displayName: 'dev' },
          challenge: 'AQID',
          pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
        },
      },
    })
    browser.create.mockResolvedValue({
      id: 'BAUG',
      rawId: buf(4, 5, 6),
      type: 'public-key',
      response: { clientDataJSON: buf(1), attestationObject: buf(2), getTransports: () => ['usb'] },
    })
    mfa.confirmMFA.mockResolvedValue({ recovery_codes: ['abcde-fghjk'], session })
    const adopt = await passwordStep({
      mfa_enrollment_required: true,
      challenge: 'kmgc_enrol',
      challenge_expires_at: '2026-10-16T12:05:00Z',
      methods: ['totp', 'webauthn'],
    })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /Security key or passkey/ }))
    })
    expect(mfa.enrollMFA).toHaveBeenCalledWith(expect.objectContaining({ kind: 'webauthn', challenge: 'kmgc_enrol' }))
    expect(screen.getByText('kubemg.example.com')).toBeTruthy()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Register key' }))
    })
    expect(mfa.confirmMFA).toHaveBeenCalledWith({
      factor_id: 12,
      challenge: 'kmgc_enrol',
      credential: expect.objectContaining({
        rawId: 'BAUG',
        response: { clientDataJSON: 'AQ', attestationObject: 'Ag', transports: ['usb'] },
      }),
    })
    fireEvent.click(screen.getByLabelText('I have saved these codes'))
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Continue to kubemg' }))
    })
    expect(adopt).toHaveBeenCalledWith('session-token', user)
  })
})

describe('the sign-in on the way to approving a kubemg login', () => {
  afterEach(() => sessionStorage.clear())

  it('says a command line is waiting, so the walk from the terminal reads as one errand', async () => {
    sessionStorage.setItem('kubemg.returnTo', '/cli/activate?code=BCDF-GHJK')
    await draw()
    expect(screen.getByText(/approve the kubemg login waiting in your terminal/)).toBeTruthy()
    expect(screen.getByText(/A command line is waiting/)).toBeTruthy()
  })

  it('is the ordinary sign-in otherwise', async () => {
    await draw()
    expect(screen.getByText('Use the account your administrator issued.')).toBeTruthy()
    expect(screen.queryByText(/A command line is waiting/)).toBeNull()
  })
})
