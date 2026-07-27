import { createContext, useContext } from 'react'
import type { User } from '../api/types'

export interface AuthState {
  user: User | null
  /** True until the stored token has been checked against the server. */
  loading: boolean
  signIn: (username: string, password: string) => Promise<void>
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
