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
}

export const AuthContext = createContext<AuthState | null>(null)

export function useAuth(): AuthState {
  const state = useContext(AuthContext)
  if (!state) {
    throw new Error('useAuth must be used inside <AuthProvider>')
  }
  return state
}
