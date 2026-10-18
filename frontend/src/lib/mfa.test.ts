import { describe, expect, it } from 'vitest'

import type { MFAStatus } from '../api/types'
import {
  factorKindLabel,
  isMFAChallenge,
  keyCanProve,
  keyIsStale,
  looksLikeTOTP,
  mfaResettable,
  mfaSummary,
  recoveryCodesFile,
  secondFactorApplies,
} from './mfa'

const enrolled: MFAStatus = {
  applies: true,
  required: false,
  enrolled: true,
  factors: [{ id: 1, kind: 'totp', name: 'phone', created_at: '2026-10-01T00:00:00Z' }],
  recovery_codes_remaining: 7,
}

describe('which accounts KubeMG’s second factor governs', () => {
  it('covers local and LDAP accounts, never OIDC, SAML or machine ones', () => {
    expect(secondFactorApplies({ auth_source: 'local', account_type: 'user' })).toBe(true)
    // A directory bind is only a password.
    expect(secondFactorApplies({ auth_source: 'ldap', account_type: 'user' })).toBe(true)
    expect(secondFactorApplies({ auth_source: 'oidc', account_type: 'user' })).toBe(false)
    expect(secondFactorApplies({ auth_source: 'saml', account_type: 'user' })).toBe(false)
    expect(secondFactorApplies({ auth_source: 'local', account_type: 'machine' })).toBe(false)
  })
})

describe('a sign-in answer', () => {
  it('is a challenge only when it carries one and no token', () => {
    expect(isMFAChallenge({ challenge: 'kmgc_x', challenge_expires_at: '', mfa_required: true })).toBe(true)
    expect(
      isMFAChallenge({ token: 't', expires_at: '', user: { id: 1 } as never }),
    ).toBe(false)
  })

  it('reads six digits with or without the app’s space as a TOTP code', () => {
    expect(looksLikeTOTP('123 456')).toBe(true)
    expect(looksLikeTOTP('123456')).toBe(true)
    expect(looksLikeTOTP('12345')).toBe(false)
    expect(looksLikeTOTP('abcde-fghjk')).toBe(false)
  })
})

describe('the access review line', () => {
  it('says a provider-held factor is the provider’s, not “none”', () => {
    expect(mfaSummary({ ...enrolled, applies: false })).toMatch(/identity provider/)
  })

  it('says none, and whether one is owed', () => {
    expect(mfaSummary({ ...enrolled, enrolled: false, factors: [] })).toBe('None — password only')
    expect(mfaSummary({ ...enrolled, enrolled: false, factors: [], required: true })).toMatch(/next sign-in/)
  })

  it('counts factors and recovery codes', () => {
    expect(mfaSummary(enrolled)).toBe('1 authenticator app · 7 recovery codes left')
  })
})

describe('when the reset is offered', () => {
  const admin = { id: 1, system_role: 'admin' as const }
  const root = { id: 2, system_role: 'superadmin' as const }
  const dev = { id: 3, system_role: 'user' as const }

  it('is never offered on your own row', () => {
    expect(mfaResettable(admin, enrolled, admin)).toBe(false)
  })

  it('is offered on a super admin only to a super admin', () => {
    expect(mfaResettable(root, enrolled, admin)).toBe(false)
    expect(mfaResettable(root, enrolled, { id: 9, system_role: 'superadmin' })).toBe(true)
  })

  it('is absent where there is nothing of kubemg’s to reset', () => {
    expect(mfaResettable(dev, { ...enrolled, enrolled: false, factors: [] }, admin)).toBe(false)
    expect(mfaResettable(dev, { ...enrolled, applies: false }, admin)).toBe(false)
    expect(mfaResettable(dev, enrolled, admin)).toBe(true)
  })
})

describe('the recovery codes file', () => {
  it('says whose codes they are and for which install', () => {
    const file = recoveryCodesFile(['abcde-fghjk'], 'dev', 'kubemg.example.com')
    expect(file).toContain('dev at kubemg.example.com')
    expect(file).toContain('abcde-fghjk')
  })
})

describe('security keys', () => {
  const withKey: MFAStatus = {
    ...enrolled,
    factors: [
      ...enrolled.factors,
      { id: 2, kind: 'webauthn', name: 'YubiKey', rp_id: 'kubemg.example.com', created_at: '2026-10-02T00:00:00Z' },
    ],
    webauthn: { available: true, rp_id: 'kubemg.example.com' },
  }

  it('counts apps and keys apart in the access review', () => {
    expect(mfaSummary(withKey)).toBe('1 authenticator app, 1 security key · 7 recovery codes left')
    expect(factorKindLabel(withKey.factors[1])).toBe('Security key')
    expect(factorKindLabel(withKey.factors[0])).toBe('Authenticator app')
  })

  it('offers a key as proof only where it answers: this host, a browser that can ask', () => {
    expect(keyCanProve(withKey, true)).toBe(true)
    expect(keyCanProve(withKey, false)).toBe(false)
    expect(keyCanProve(enrolled, true)).toBe(false)
    const moved = { ...withKey, webauthn: { available: true, rp_id: 'console.example.net' } }
    expect(keyCanProve(moved, true)).toBe(false)
    expect(keyIsStale(withKey.factors[1], moved)).toBe(true)
    expect(keyIsStale(withKey.factors[1], withKey)).toBe(false)
    // An app is never stale; it is bound to nothing.
    expect(keyIsStale(withKey.factors[0], moved)).toBe(false)
  })
})
