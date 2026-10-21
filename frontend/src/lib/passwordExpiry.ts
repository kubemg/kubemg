import type { User } from '../api/types'
import type { Tone } from './status'
import { formatInstant } from './time'

/** Whether the password is one KubeMG holds, and so one its policy governs. */
export function passwordHeldHere(user: Pick<User, 'auth_source' | 'account_type'>): boolean {
  return user.auth_source === 'local' && user.account_type !== 'machine'
}

/**
 * Whether the caller may expire target's password — the server's rules,
 * mirrored so the control is absent rather than refused: never your own, never
 * a password held elsewhere, and only a super admin acts on a super admin. An
 * account already held at the change screen has nothing left to expire.
 */
export function passwordExpirable(
  target: Pick<User, 'id' | 'system_role' | 'auth_source' | 'account_type' | 'password_change_required'>,
  caller: Pick<User, 'id' | 'system_role'> | null,
): boolean {
  if (!caller || caller.id === target.id) return false
  if (!passwordHeldHere(target) || target.password_change_required) return false
  if (target.system_role === 'superadmin' && caller.system_role !== 'superadmin') return false
  return true
}

export interface PasswordStatus {
  tone: Tone
  text: string
}

const DAY = 24 * 60 * 60 * 1000

/** One line on where an account's password stands against the policy. */
export function passwordStatus(
  user: Pick<User, 'auth_source' | 'account_type' | 'password_expires_at' | 'password_change_required'>,
  now: Date = new Date(),
): PasswordStatus {
  if (!passwordHeldHere(user)) return { tone: 'idle', text: 'Held by its identity provider' }
  if (user.password_change_required) {
    return {
      tone: 'bad',
      text: user.password_expires_at
        ? `Expired ${formatInstant(user.password_expires_at)} — must change at next sign-in`
        : 'Expired — must change at next sign-in',
    }
  }
  if (!user.password_expires_at) return { tone: 'idle', text: 'Does not expire' }
  const left = Math.ceil((new Date(user.password_expires_at).getTime() - now.getTime()) / DAY)
  const when = formatInstant(user.password_expires_at)
  if (left <= 14) {
    return { tone: 'warn', text: `Expires in ${left} ${left === 1 ? 'day' : 'days'} (${when})` }
  }
  return { tone: 'ok', text: `Expires ${when}` }
}
