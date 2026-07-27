import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { fetchMe, login, readToken, setUnauthorizedHandler, writeToken } from '../api/client'
import type { User } from '../api/types'
import { AuthContext } from './auth-context'
import type { AuthState } from './auth-context'

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)

  const signOut = useCallback(() => {
    writeToken(null)
    setUser(null)
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

  const value = useMemo<AuthState>(
    () => ({ user, loading, signIn, signOut }),
    [user, loading, signIn, signOut],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
