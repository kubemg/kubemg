import { AxiosError, AxiosHeaders } from 'axios'
import { describe, expect, it } from 'vitest'

import {
  SIGN_IN_TEXT,
  callbackErrorText,
  readSignInFailure,
  signInErrorText,
} from './signInError'

/*
 * The refusals exactly as the server writes them (backend/pkg/api/auth.go and
 * sso.go). The table under test is keyed on these — the status, and on the
 * directory route the account sync's sentinels — so a change on either side
 * shows up here.
 */
const local = {
  wrongPassword: { status: 401, message: 'invalid credentials' },
  unknownUser: { status: 401, message: 'invalid credentials' },
  federated: { status: 401, message: 'invalid credentials' },
  machine: { status: 401, message: 'invalid credentials' },
  disabled: { status: 403, message: 'this account is disabled' },
  missingField: { status: 400, message: 'username and password are required' },
  storeDown: { status: 500, message: 'could not verify credentials' },
}

function axiosFailure(status: number, data: unknown): AxiosError {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('Request failed', 'ERR_BAD_REQUEST', config, {}, {
    status,
    statusText: '',
    headers: {},
    config,
    data,
  })
}

describe('signInErrorText on the local form', () => {
  it('reads a wrong password and an unknown username identically', () => {
    // The server answers both with the same 401 so it is not an enumeration
    // oracle; the console must not become one either.
    const wrong = signInErrorText(local.wrongPassword, 'local')
    expect(signInErrorText(local.unknownUser, 'local')).toBe(wrong)
    // A federated or machine account signing in here is, by the server's
    // design, indistinguishable from a wrong password.
    expect(signInErrorText(local.federated, 'local')).toBe(wrong)
    expect(signInErrorText(local.machine, 'local')).toBe(wrong)
    expect(wrong).toBe(SIGN_IN_TEXT.credentials)
  })

  it('adds the provider hint to every credentials refusal alike, and only when a provider exists', () => {
    const withProviders = signInErrorText(local.wrongPassword, 'local', { hasProviders: true })
    expect(withProviders).toContain(SIGN_IN_TEXT.credentialsProviderHint)
    expect(signInErrorText(local.unknownUser, 'local', { hasProviders: true })).toBe(withProviders)
    expect(signInErrorText(local.wrongPassword, 'local')).not.toContain(
      SIGN_IN_TEXT.credentialsProviderHint,
    )
    expect(signInErrorText(local.disabled, 'local', { hasProviders: true })).toBe(
      SIGN_IN_TEXT.disabled,
    )
  })

  it('keeps the disabled account as its own refusal', () => {
    expect(signInErrorText(local.disabled, 'local')).toBe(SIGN_IN_TEXT.disabled)
    expect(SIGN_IN_TEXT.disabled).not.toBe(SIGN_IN_TEXT.credentials)
  })

  it('names the incomplete form, the unavailable server and the unreachable one', () => {
    expect(signInErrorText(local.missingField, 'local')).toBe(SIGN_IN_TEXT.incomplete)
    expect(signInErrorText(local.storeDown, 'local')).toBe(SIGN_IN_TEXT.unavailable)
    expect(signInErrorText({}, 'local')).toBe(SIGN_IN_TEXT.unreachable)
  })

  it('falls back to a console sentence for a shape it does not know', () => {
    expect(signInErrorText({ status: 418, message: 'teapot' }, 'local')).toBe(SIGN_IN_TEXT.generic)
  })

  it('never puts the server string on screen', () => {
    const everything = [
      ...Object.values(local).flatMap((failure) => [
        signInErrorText(failure, 'local'),
        signInErrorText(failure, 'local', { hasProviders: true }),
        signInErrorText(failure, 'directory'),
      ]),
    ]
    for (const text of everything) {
      expect(text).not.toMatch(/invalid credentials/i)
      expect(text).not.toBe('this account is disabled')
    }
  })
})

describe('signInErrorText on the directory form', () => {
  it('says the directory refused the password, and that an unreachable directory is not a wrong one', () => {
    expect(signInErrorText({ status: 401, message: 'invalid credentials' }, 'directory')).toBe(
      SIGN_IN_TEXT.directoryCredentials,
    )
    expect(signInErrorText({ status: 502, message: 'ldap: dial tcp: i/o timeout' }, 'directory')).toBe(
      SIGN_IN_TEXT.directoryUnreachable,
    )
  })

  it('reads each of the account sync refusals as itself', () => {
    expect(
      signInErrorText(
        { status: 403, message: 'no KubeMG account is provisioned for this identity' },
        'directory',
      ),
    ).toBe(SIGN_IN_TEXT.noAccount)
    expect(signInErrorText({ status: 403, message: 'this account is disabled' }, 'directory')).toBe(
      SIGN_IN_TEXT.disabled,
    )
    expect(
      signInErrorText(
        { status: 409, message: 'an account with this username already exists' },
        'directory',
      ),
    ).toBe(SIGN_IN_TEXT.conflict)
    expect(
      signInErrorText(
        {
          status: 403,
          message:
            "the identity provider asserted a username containing ':' or control characters, which no KubeMG account may carry — configure the provider's username claim to one without them",
        },
        'directory',
      ),
    ).toBe(SIGN_IN_TEXT.unsafeUsername)
    expect(
      signInErrorText({ status: 403, message: 'this identity provider is disabled' }, 'directory'),
    ).toBe(SIGN_IN_TEXT.providerOff)
  })

  it('reads a refusal it does not recognise as a refusal, not as the server string', () => {
    expect(signInErrorText({ status: 403, message: 'something new' }, 'directory')).toBe(
      SIGN_IN_TEXT.accountRefused,
    )
  })
})

describe('readSignInFailure', () => {
  it('reads the status and the error field off a refused request', () => {
    expect(readSignInFailure(axiosFailure(401, { error: 'invalid credentials' }))).toEqual({
      status: 401,
      message: 'invalid credentials',
    })
  })

  it('reads a request that never got an answer as having no status', () => {
    const config = { headers: new AxiosHeaders() }
    expect(readSignInFailure(new AxiosError('Network Error', 'ERR_NETWORK', config, {}))).toEqual({
      status: undefined,
      message: undefined,
    })
    expect(readSignInFailure(new Error('boom'))).toEqual({})
  })
})

describe('callbackErrorText', () => {
  it('puts the account sync refusals in the console words', () => {
    expect(callbackErrorText('this account is disabled')).toBe(SIGN_IN_TEXT.disabled)
    expect(callbackErrorText('no KubeMG account is provisioned for this identity')).toBe(
      SIGN_IN_TEXT.noAccount,
    )
  })

  it('keeps a sentence the server already wrote for the person', () => {
    const expired = 'This sign-in request has expired. Please try again.'
    expect(callbackErrorText(expired)).toBe(expired)
  })
})
