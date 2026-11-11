import { useCallback, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { useAuth } from './auth-context'
import { ShellDockContext } from './shell-dock-context'

/** ShellDockProvider holds which cluster's shell is open, above the router so a
    session survives navigating around the console. */
export function ShellDockProvider({ children }: { children: ReactNode }) {
  const [clusterId, setClusterId] = useState<number | null>(null)
  const [clusterName, setClusterName] = useState('')
  const [open, setOpen] = useState(false)
  const [expanded, setExpanded] = useState(false)

  // A shell is its owner's session. Signing out, or somebody else signing in
  // at the same browser, closes the dock and forgets the cluster, so the next
  // person never sees, or types into, the previous person's terminal. Reset
  // during render rather than in an effect: the old session must not be
  // drawn for even one frame under the new identity.
  const { user } = useAuth()
  const userId = user?.id ?? null
  const [owner, setOwner] = useState(userId)
  if (owner !== userId) {
    setOwner(userId)
    setOpen(false)
    setClusterId(null)
    setClusterName('')
    setExpanded(false)
  }

  // Moving the dock to another cluster is a different session. The dock tears
  // the old one down when the id changes rather than re-labelling it: a terminal
  // that silently changed which cluster it was talking to would be the worst bug
  // this feature could have.
  const openOn = useCallback((id: number, name: string) => {
    setClusterId(id)
    setClusterName(name)
    setOpen(true)
  }, [])

  // Decided from the current values, not inside a state updater: an updater
  // must be pure, and StrictMode runs it twice.
  const toggle = useCallback(
    (id: number, name: string) => {
      if (open && clusterId === id) {
        setOpen(false)
        return
      }
      setClusterId(id)
      setClusterName(name)
      setOpen(true)
    },
    [open, clusterId],
  )

  const close = useCallback(() => setOpen(false), [])

  const value = useMemo(
    () => ({ open, clusterId, clusterName, expanded, openOn, toggle, close, setExpanded }),
    [open, clusterId, clusterName, expanded, openOn, toggle, close],
  )

  return <ShellDockContext.Provider value={value}>{children}</ShellDockContext.Provider>
}
