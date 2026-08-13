import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import {
  fetchMe,
  fetchSetupState,
  login,
  readToken,
  setUnauthorizedHandler,
  writeToken,
} from '../api/client'
import type { User } from '../api/types'
import { invalidateQueries } from '../lib/query'
import { AuthContext } from './auth-context'
import type { AuthState } from './auth-context'

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)

  // Whether this server has been through first-run setup. It is asked once, on
  // load, and unauthenticated — the sign-in page says so before anybody has a
  // session, and the console redirects an administrator into the wizard the
  // moment they have one.
  const [setupRequired, setSetupRequired] = useState(false)
  const [setupLoading, setSetupLoading] = useState(true)

  const refreshSetupState = useCallback(async () => {
    const state = await fetchSetupState()
    setSetupRequired(state.required)
    setSetupLoading(false)
  }, [])

  useEffect(() => {
    void refreshSetupState()
  }, [refreshSetupState])

  const signOut = useCallback(() => {
    writeToken(null)
    setUser(null)
    // Cached reads belong to the identity that asked for them. The next person
    // at this browser is a different identity, so nothing is carried over.
    invalidateQueries()
  }, [])

  // Restore the session from a stored token, dropping it if the server no
  // longer honours it.
  useEffect(() => {
    if (!readToken()) {
      setLoading(false)
      return
    }

    let active = true
    fetchMe()
      .then((me) => {
        if (active) setUser(me)
      })
      .catch(() => {
        if (active) signOut()
      })
      .finally(() => {
        if (active) setLoading(false)
      })

    return () => {
      active = false
    }
  }, [signOut])

  useEffect(() => {
    setUnauthorizedHandler(signOut)
    return () => setUnauthorizedHandler(null)
  }, [signOut])

  const signIn = useCallback(async (username: string, password: string) => {
    const session = await login(username, password)
    writeToken(session.token)
    setUser(session.user)
  }, [])

  const adoptSession = useCallback(async (token: string, session?: User) => {
    writeToken(token)
    if (session) {
      setUser(session)
      return
    }
    // A token that arrived in a URL fragment says nothing trustworthy about who
    // it belongs to, so the account is read back from the server — which also
    // catches a token that is already expired or has been revoked.
    try {
      setUser(await fetchMe())
    } catch (err) {
      writeToken(null)
      throw err
    }
  }, [])

  const value = useMemo<AuthState>(
    () => ({
      user,
      loading,
      signIn,
      adoptSession,
      signOut,
      setupRequired,
      setupLoading,
      refreshSetupState,
    }),
    [
      user,
      loading,
      signIn,
      adoptSession,
      signOut,
      setupRequired,
      setupLoading,
      refreshSetupState,
    ],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
