import { createContext, useContext } from 'react'

/**
 * The browser shell's dock.
 *
 * The shell is deliberately **not** a page. An operator reaches for a terminal
 * in the middle of a question — while reading a workload's events, while
 * looking at a failing rollout — and a page would take the thing they are
 * asking about off the screen to answer it. So it opens as a layer over the
 * console, the shape AWS CloudShell and a browser's own devtools both settle
 * on, and it keeps running while they navigate.
 *
 * That last part is why this is a context rather than component state: `AppShell`
 * is rendered by each page, so a dock living inside it would be torn down and
 * its session dropped on every click. The dock is mounted once, above the
 * router's outlet, and the header button reaches it through here.
 */
export interface ShellDockState {
  /** Whether the dock is on screen. */
  open: boolean
  /** Which cluster the session belongs to; null when nothing has been opened. */
  clusterId: number | null
  /** Its name, carried here rather than looked up: the dock is mounted above the
      router — which is what keeps a session alive across navigation — and the
      cluster list is a provider that lives below it. */
  clusterName: string
  /** Half the viewport, or nearly all of it. */
  expanded: boolean
  /** Opens the dock on a cluster, or brings it back if it is already that one. */
  openOn: (clusterId: number, name: string) => void
  /** The header button: the same cluster closes it, a different one moves it. */
  toggle: (clusterId: number, name: string) => void
  close: () => void
  setExpanded: (expanded: boolean) => void
}

export const ShellDockContext = createContext<ShellDockState | null>(null)

export function useShellDock(): ShellDockState {
  const state = useContext(ShellDockContext)
  if (!state) {
    throw new Error('useShellDock must be used inside <ShellDockProvider>')
  }
  return state
}
