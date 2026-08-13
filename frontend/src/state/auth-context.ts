import { createContext, useContext } from 'react'
import type { User } from '../api/types'

export interface AuthState {
  user: User | null
  /** True until the stored token has been checked against the server. */
  loading: boolean
  signIn: (username: string, password: string) => Promise<void>
  /**
   * Adopt a session issued somewhere other than the password form: an LDAP
   * provider's own check, or an interactive sign-in coming back from an IdP with
   * the token in the callback fragment. The user is read back from the server
   * rather than trusted from the URL.
   */
  adoptSession: (token: string, user?: User) => Promise<void>
  signOut: () => void
  /**
   * Whether this server still needs first-run setup. It lives here rather than
   * in a provider of its own because it is the same phase of the session as
   * `loading` is: the console cannot decide what to render until both the
   * identity and this are known, and the sign-in page needs it before there is
   * an identity at all.
   */
  setupRequired: boolean
  /** True until that answer has come back. */
  setupLoading: boolean
  /** Re-read it after the wizard finishes, which is what closes the gate. */
  refreshSetupState: () => Promise<void>
}

export const AuthContext = createContext<AuthState | null>(null)

export function useAuth(): AuthState {
  const state = useContext(AuthContext)
  if (!state) {
    throw new Error('useAuth must be used inside <AuthProvider>')
  }
  return state
}
