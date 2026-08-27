import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Power, RefreshCw, SquareTerminal, Terminal } from 'lucide-react'
import { errorMessage, fetchShell, startShell, stopShell } from '../api/client'
import type { ShellState } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, EmptyState, Notice, Pill } from '../components/primitives'
import { shellLifetime, shellReach, shellSecondsLeft, shellView } from '../lib/shell'
import { formatDuration } from '../lib/time'
import { useClusters } from '../state/clusters-context'

/*
 * The browser shell, as a page.
 *
 * It is a page rather than a drawer for the reason Explore's terminal is not one
 * either: a shell is where somebody *works*, sometimes for an hour, and a
 * surface that closes when the operator clicks past it is a surface they cannot
 * work in. It is also the one place in this console where the thing on screen is
 * a running process rather than a read, which is why the whole state machine —
 * absent, starting, ready, ended — is drawn rather than collapsed into a
 * spinner: "the image is still pulling on this node" and "this cluster has no
 * agent" are the two answers an operator actually needs, and a spinner gives
 * neither.
 *
 * The disclosure above the terminal is not decoration. What a shell can reach is
 * the caller's own grant, what it costs is two clocks, and what it keeps is
 * nothing — all three said before the first keystroke rather than discovered by
 * being refused.
 */

// xterm is ~290kB. It is loaded when somebody opens a shell and never as part of
// the main bundle — the same rule the pod terminal follows.
const ShellTerminal = lazy(() =>
  import('../components/ShellTerminal').then((module) => ({ default: module.ShellTerminal })),
)

/** How often the page re-reads a pod that is still coming up. */
const STARTING_POLL_MS = 3000

export function ClusterShell() {
  const { clusters, loading: clustersLoading } = useClusters()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)

  const cluster = useMemo(
    () => clusters.find((entry) => entry.id === clusterId) ?? null,
    [clusters, clusterId],
  )

  const [state, setState] = useState<ShellState | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [attached, setAttached] = useState(false)

  const read = useCallback(async () => {
    if (!Number.isFinite(clusterId)) return
    try {
      setState(await fetchShell(clusterId))
      setError(null)
    } catch (cause) {
      setError(errorMessage(cause, 'Could not read the shell.'))
    }
  }, [clusterId])

  useEffect(() => {
    void read()
  }, [read])

  const view = shellView(state ?? undefined)

  // A pod that is coming up is the one state worth polling: the answer changes
  // on its own, and the operator is waiting on it. Nothing else here polls —
  // a ready shell is watched by the socket, and an absent one changes only when
  // somebody presses the button.
  useEffect(() => {
    if (view?.kind !== 'starting') return
    const timer = window.setInterval(() => void read(), STARTING_POLL_MS)
    return () => window.clearInterval(timer)
  }, [view?.kind, read])

  async function start() {
    setBusy(true)
    setError(null)
    try {
      const next = await startShell(clusterId)
      setState(next)
      if (next.status.ready) setAttached(true)
    } catch (cause) {
      setError(errorMessage(cause, 'Could not start a shell.'))
    } finally {
      setBusy(false)
    }
  }

  async function stop() {
    setBusy(true)
    setError(null)
    setAttached(false)
    try {
      await stopShell(clusterId)
      await read()
    } catch (cause) {
      setError(errorMessage(cause, 'Could not end the shell.'))
    } finally {
      setBusy(false)
    }
  }

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Shell">
        <div className="card">
          <EmptyState
            icon={<SquareTerminal aria-hidden="true" className="size-5" />}
            title="That cluster is not registered"
          >
            Pick a cluster from the fleet list to open a shell on it.
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const secondsLeft = state ? shellSecondsLeft(state) : null

  return (
    <AppShell
      title="Shell"
      fullWidth
      actions={
        <div className="flex items-center gap-2">
          <Button onClick={() => void read()} disabled={busy}>
            <RefreshCw aria-hidden="true" className="size-4" />
            Refresh
          </Button>
          {view?.kind === 'ready' || view?.kind === 'starting' ? (
            <Button variant="danger" onClick={() => void stop()} disabled={busy}>
              <Power aria-hidden="true" className="size-4" />
              End shell
            </Button>
          ) : null}
        </div>
      }
    >
      <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {view?.kind === 'disabled' || view?.kind === 'unavailable' ? (
          <div className="card">
            <EmptyState
              icon={<SquareTerminal aria-hidden="true" className="size-5" />}
              title={view.kind === 'disabled' ? 'The browser shell is switched off' : 'No shell on this cluster'}
            >
              {view.reason}
              {view.kind === 'unavailable' && cluster ? (
                <>
                  {' '}
                  <Link to={`/clusters/${cluster.id}/summary`} className="text-accent hover:underline">
                    Open the cluster
                  </Link>{' '}
                  to check how it is registered.
                </>
              ) : null}
            </EmptyState>
          </div>
        ) : null}

        {state && (view?.kind === 'absent' || view?.kind === 'ended') ? (
          <div className="card flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Terminal aria-hidden="true" className="size-4 text-muted" />
              <h2 className="text-[14px] text-fg">A terminal on {cluster?.name}</h2>
            </div>
            <p className="text-[13px] text-muted">
              <code className="text-fg">kubectl</code> and <code className="text-fg">helm</code>, in a pod
              KubeMG runs on this cluster. It holds no cluster credential of its own: its kubeconfig points
              back at this server, so everything it runs goes down the same audited tunnel as the rest of
              the console.
            </p>
            <p className="text-[13px] text-muted">{shellReach(state)}</p>
            <p className="text-[13px] text-muted">{shellLifetime(state)}</p>
            {view.kind === 'ended' ? (
              <Notice tone="warn">
                The previous shell has ended{view.message ? `: ${view.message}` : ''}. Starting a new one
                clears it away.
              </Notice>
            ) : null}
            <div className="flex items-center gap-2">
              <Button variant="primary" onClick={() => void start()} disabled={busy}>
                <SquareTerminal aria-hidden="true" className="size-4" />
                {busy ? 'Starting…' : 'Start a shell'}
              </Button>
              {state.image ? <span className="text-[12px] text-faint">{state.image}</span> : null}
            </div>
          </div>
        ) : null}

        {view?.kind === 'starting' ? (
          <Notice tone="info">
            The shell is starting{view.message ? ` — the cluster reports ${view.message}` : ''}. A node that
            has never pulled this image takes a moment.
          </Notice>
        ) : null}

        {state && view?.kind === 'ready' ? (
          <>
            <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted">
              <Pill tone="ok">running</Pill>
              <span>{shellReach(state)}</span>
              {secondsLeft !== null ? (
                <span className="text-faint">· ends in {formatDuration(secondsLeft)}</span>
              ) : null}
            </div>

            {attached ? (
              <Suspense
                fallback={<div className="card text-[13px] text-muted">Loading the terminal…</div>}
              >
                <ShellTerminal clusterId={clusterId} onEnded={() => void read()} />
              </Suspense>
            ) : (
              <div className="card flex flex-col gap-3">
                <p className="text-[13px] text-muted">{shellLifetime(state)}</p>
                <div>
                  <Button variant="primary" onClick={() => setAttached(true)}>
                    <SquareTerminal aria-hidden="true" className="size-4" />
                    Attach
                  </Button>
                </div>
              </div>
            )}
          </>
        ) : null}
      </div>
    </AppShell>
  )
}
