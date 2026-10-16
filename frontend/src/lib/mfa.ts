import type { MFAChallenge, MFAFactor, MFAStatus, SignInResult, User } from '../api/types'

/** Whether a password sign-in was answered with a challenge rather than a session. */
export function isMFAChallenge(result: SignInResult): result is MFAChallenge {
  return 'challenge' in result && typeof result.challenge === 'string' && !('token' in result && result.token)
}

/**
 * Whether KubeMG's own second factor governs an account — the server's
 * `User.MFAApplies`, mirrored so a door the server would refuse is never drawn.
 * A local account and an LDAP one (a directory bind is only a password); not an
 * OIDC or SAML account, whose factor is its provider's, and never a machine
 * account, which has no password at all.
 */
export function secondFactorApplies(user: Pick<User, 'auth_source' | 'account_type'>): boolean {
  if (user.account_type === 'machine') return false
  return user.auth_source === 'local' || user.auth_source === 'ldap' || !user.auth_source
}

/** A six-digit code, typed with or without the app's space. */
export function looksLikeTOTP(code: string): boolean {
  return /^\d{6}$/.test(code.replace(/[\s-]/g, ''))
}

/**
 * The recovery codes as a file somebody saves or prints. It says what they are
 * for and which install, because a file called `recovery-codes.txt` found a
 * year later answers neither.
 */
export function recoveryCodesFile(codes: string[], account: string, host: string): string {
  return [
    `kubemg recovery codes for ${account} at ${host}`,
    'Each code signs in once in place of your authenticator app or security key. Keep them somewhere other than either.',
    '',
    ...codes,
    '',
  ].join('\n')
}

/**
 * The second factor in one line. Said for every account, because a review has
 * to know what was checked: an OIDC or SAML account's factor is its provider's
 * and this console never sees it, which is a different answer from "none".
 */
export function mfaSummary(mfa: MFAStatus | null): string {
  if (!mfa) return 'Not read'
  if (!mfa.applies) return 'Held by the identity provider — kubemg does not see it'
  if (!mfa.enrolled) return mfa.required ? 'None — must set one up at the next sign-in' : 'None — password only'
  const apps = mfa.factors.filter((factor) => factor.kind !== 'webauthn').length
  const keys = mfa.factors.length - apps
  const codes = mfa.recovery_codes_remaining
  const held = [
    apps ? `${apps} authenticator ${apps === 1 ? 'app' : 'apps'}` : '',
    keys ? `${keys} security ${keys === 1 ? 'key' : 'keys'}` : '',
  ].filter(Boolean)
  return `${held.join(', ')} · ${codes} recovery ${codes === 1 ? 'code' : 'codes'} left`
}

/** What a factor is, in the profile's words. */
export function factorKindLabel(factor: Pick<MFAFactor, 'kind'>): string {
  return factor.kind === 'webauthn' ? 'Security key' : 'Authenticator app'
}

/**
 * Whether a key no longer works here: it was registered for a host other than
 * the one the public URL names today, and a key answers only to its own.
 */
export function keyIsStale(factor: Pick<MFAFactor, 'kind' | 'rp_id'>, mfa: Pick<MFAStatus, 'webauthn'>): boolean {
  const current = mfa.webauthn?.rp_id
  return factor.kind === 'webauthn' && Boolean(current) && factor.rp_id !== current
}

/**
 * Whether a change to the factors can be proved with a key here: the account
 * holds one bound to this host, and the install and the browser can both use
 * it. The browser half is passed in so this stays a pure derivation.
 */
export function keyCanProve(mfa: Pick<MFAStatus, 'factors' | 'webauthn'>, browserCanAsk: boolean): boolean {
  if (!browserCanAsk || !mfa.webauthn?.available) return false
  return mfa.factors.some((factor) => factor.kind === 'webauthn' && factor.rp_id === mfa.webauthn?.rp_id)
}

/**
 * Whether the reset is offered. Absent rather than disabled where the server
 * would refuse it: on your own row, on a super admin unless you are one, and
 * where there is nothing of KubeMG's to reset.
 */
export function mfaResettable(
  target: Pick<User, 'id' | 'system_role'>,
  mfa: MFAStatus,
  caller: Pick<User, 'id' | 'system_role'> | null,
): boolean {
  if (!caller || caller.id === target.id) return false
  if (!mfa.applies || !mfa.enrolled) return false
  if (target.system_role === 'superadmin' && caller.system_role !== 'superadmin') return false
  return true
}
