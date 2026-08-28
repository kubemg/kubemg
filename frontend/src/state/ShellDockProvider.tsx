import { useCallback, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { ShellDockContext } from './shell-dock-context'

/** ShellDockProvider holds which cluster's shell is open, above the router so a
    session survives navigating around the console. */
export function ShellDockProvider({ children }: { children: ReactNode }) {
  const [clusterId, setClusterId] = useState<number | null>(null)
  const [clusterName, setClusterName] = useState('')
  const [open, setOpen] = useState(false)
  const [expanded, setExpanded] = useState(false)

  // Moving the dock to another cluster is a different session. The dock tears
  // the old one down when the id changes rather than re-labelling it: a terminal
  // that silently changed which cluster it was talking to would be the worst bug
  // this feature could have.
  const openOn = useCallback((id: number, name: string) => {
    setClusterId(id)
    setClusterName(name)
    setOpen(true)
  }, [])

  const toggle = useCallback(
    (id: number, name: string) => {
      setOpen((current) => {
        if (current && clusterId === id) return false
        setClusterId(id)
        setClusterName(name)
        return true
      })
    },
    [clusterId],
  )

  const close = useCallback(() => setOpen(false), [])

  const value = useMemo(
    () => ({ open, clusterId, clusterName, expanded, openOn, toggle, close, setExpanded }),
    [open, clusterId, clusterName, expanded, openOn, toggle, close],
  )

  return <ShellDockContext.Provider value={value}>{children}</ShellDockContext.Provider>
}
