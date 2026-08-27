import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { Maximize2, Minimize2, Power, X } from 'lucide-react'
import { errorMessage, fetchShell, startShell, stopShell } from '../api/client'
import type { ShellState } from '../api/types'
import { IconButton, Pill } from './primitives'
import { shellLifetime, shellReach, shellSecondsLeft, shellView } from '../lib/shell'
import { formatDuration } from '../lib/time'
import { useShellDock } from '../state/shell-dock-context'

/*
 * The shell dock.
 *
 * A terminal is reached for in the middle of a question — while reading a
 * workload's events, while watching a rollout stall — so it opens as a layer
 * over the console rather than as a page that takes the thing being asked about
 * off the screen. It is mounted once, above the router, so the session keeps
 * running while its operator navigates.
 *
 * **Opening it opens a shell.** There is no card in between explaining what a
 * shell is and offering a button: the operator pressed the button that says
 * Shell, and a second one is a step that exists only to carry prose. What has to
 * be said is said on the strip above the prompt, in a line — what the session can
 * reach, its two clocks, and that nothing in it is kept — plus the recording
 * notice the terminal draws for itself. That is the same disclosure, in the
 * place somebody actually reads it.
 */

// xterm is ~290kB and must never be in the main bundle: the dock is mounted on
// every page, and the terminal is loaded when a session is genuinely opening.
const ShellTerminal = lazy(() =>
  import('./ShellTerminal').then((module) => ({ default: module.ShellTerminal })),
)

/** How often a pod that is still coming up is re-read. */
const STARTING_POLL_MS = 3000

export function ShellDock() {
  const dock = useShellDock()

  const [state, setState] = useState<ShellState | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const clusterId = dock.clusterId

  // Which cluster the state on screen belongs to. Without this a dock moved to
  // another cluster would draw the previous one's session for a frame — which,
  // for a terminal, is the one wrong thing it must never do.
  const shownFor = useRef<number | null>(null)

  const read = useCallback(async (id: number) => {
    try {
      const next = await fetchShell(id)
      setState(next)
      setError(null)
      return next
    } catch (cause) {
      setError(errorMessage(cause, 'Could not read the shell.'))
      return null
    }
  }, [])

  // Opening the dock starts a shell. It is idempotent on the server — the pod's
  // name is derived from the user — so re-opening finds the session that is
  // already there rather than making a second one.
  useEffect(() => {
    if (!dock.open || clusterId === null) return
    if (shownFor.current === clusterId) return

    shownFor.current = clusterId
    setState(null)
    setError(null)
    setBusy(true)

    let cancelled = false
    void (async () => {
      try {
        const next = await startShell(clusterId)
        if (!cancelled) setState(next)
      } catch (cause) {
        if (!cancelled) setError(errorMessage(cause, 'Could not start a shell.'))
      } finally {
        if (!cancelled) setBusy(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [dock.open, clusterId])

  const view = shellView(state ?? undefined)

  // A pod that is coming up is the one state worth polling: it changes on its
  // own and somebody is waiting on it. A running session is watched by its
  // socket, and a closed dock is watched by nobody.
  useEffect(() => {
    if (!dock.open || clusterId === null || view?.kind !== 'starting') return
    const timer = window.setInterval(() => void read(clusterId), STARTING_POLL_MS)
    return () => window.clearInterval(timer)
  }, [dock.open, clusterId, view?.kind, read])

  async function end() {
    if (clusterId === null) return
    setBusy(true)
    try {
      await stopShell(clusterId)
      shownFor.current = null
      setState(null)
      dock.close()
    } catch (cause) {
      setError(errorMessage(cause, 'Could not end the shell.'))
    } finally {
      setBusy(false)
    }
  }

  if (!dock.open || clusterId === null) return null

  const secondsLeft = state ? shellSecondsLeft(state) : null

  return (
    <section
      aria-label="Shell"
      className={`fixed inset-x-0 bottom-0 z-30 flex flex-col border-t border-line bg-bg shadow-[0_-12px_32px_rgba(0,0,0,0.28)] ${
        dock.expanded ? 'h-[85vh]' : 'h-[46vh]'
      }`}
    >
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-soft px-3">
        <span className="min-w-0 truncate text-[13px] font-medium text-fg">
          {dock.clusterName || 'Shell'}
        </span>

        {view?.kind === 'ready' ? (
          <Pill tone="ok">running</Pill>
        ) : view?.kind === 'starting' ? (
          <Pill tone="warn">starting</Pill>
        ) : null}

        {/* The disclosure, on the line above the prompt rather than in a card in
            front of it. */}
        {state && view && (view.kind === 'ready' || view.kind === 'starting') ? (
          <span className="hidden min-w-0 truncate text-[12px] text-muted md:inline">
            {shellReach(state)} · {shellLifetime(state)}
          </span>
        ) : null}

        {secondsLeft !== null && view?.kind === 'ready' ? (
          <span className="hidden shrink-0 text-[12px] text-faint lg:inline">
            ends in {formatDuration(secondsLeft)}
          </span>
        ) : null}

        <div className="ml-auto flex shrink-0 items-center gap-1">
          {view?.kind === 'ready' || view?.kind === 'starting' ? (
            <IconButton label="End this shell" tone="danger" disabled={busy} onClick={() => void end()}>
              <Power aria-hidden="true" className="size-4" />
            </IconButton>
          ) : null}
          <IconButton
            label={dock.expanded ? 'Shrink the shell' : 'Expand the shell'}
            onClick={() => dock.setExpanded(!dock.expanded)}
          >
            {dock.expanded ? (
              <Minimize2 aria-hidden="true" className="size-4" />
            ) : (
              <Maximize2 aria-hidden="true" className="size-4" />
            )}
          </IconButton>
          {/* Closing hides the dock and leaves the session running: it is
              reclaimed by its own idle window, and somebody who closed a panel
              did not ask for the command in it to be killed. Ending it is the
              button beside this one, and it says so. */}
          <IconButton label="Hide the shell" onClick={dock.close}>
            <X aria-hidden="true" className="size-4" />
          </IconButton>
        </div>
      </header>

      <div className="flex min-h-0 flex-1 flex-col gap-2 p-3">
        {error ? <DockMessage tone="danger">{error}</DockMessage> : null}

        {!error && (busy || !state) ? <DockMessage>Starting a shell…</DockMessage> : null}

        {view?.kind === 'disabled' || view?.kind === 'unavailable' ? (
          <DockMessage>{view.reason}</DockMessage>
        ) : null}

        {view?.kind === 'starting' ? (
          <DockMessage>
            The shell is starting{view.message ? ` — the cluster reports ${view.message}` : ''}. A node
            that has never pulled this image takes a moment.
          </DockMessage>
        ) : null}

        {view?.kind === 'ended' ? (
          <DockMessage>
            The shell ended{view.message ? `: ${view.message}` : ''}. Close and open the dock to start
            a new one.
          </DockMessage>
        ) : null}

        {view?.kind === 'ready' ? (
          <Suspense fallback={<DockMessage>Loading the terminal…</DockMessage>}>
            <ShellTerminal clusterId={clusterId} onEnded={() => void read(clusterId)} />
          </Suspense>
        ) : null}
      </div>
    </section>
  )
}

/** One line of explanation inside the dock — never a card, never a dialog: this
    is a strip over somebody's work, and it has room for a sentence. */
function DockMessage({ children, tone = 'muted' }: { children: React.ReactNode; tone?: 'muted' | 'danger' }) {
  return (
    <p className={`text-[13px] ${tone === 'danger' ? 'text-danger' : 'text-muted'}`}>{children}</p>
  )
}
