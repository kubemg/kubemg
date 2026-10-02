import type { User } from '../api/types'

/**
 * Which of the owner's own acts this account can take in the console.
 *
 * Both need the account's details to live here: a federated account's belong to
 * its directory (written back at every sign-in, its password held by the
 * provider) and a machine account is administered and holds no password. The
 * server refuses both with a 409; the console draws no door to the refusal.
 */
export function profileAbilities(user: User): { editDetails: boolean; changePassword: boolean } {
  const local = user.auth_source === 'local' && user.account_type !== 'machine'
  return { editDetails: local, changePassword: local }
}
