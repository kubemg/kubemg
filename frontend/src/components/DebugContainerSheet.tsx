import { useEffect, useRef, useState } from 'react'
import { Bug, Loader2 } from 'lucide-react'
import { debugPodContainer, errorMessage, fetchPod } from '../api/client'
import type { Cluster, DebugContainerResult, Pod } from '../api/types'
import { Button, Notice, Select, Sheet } from './primitives'

/*
 * The pod that has no shell.
 *
 * `exec` needs a shell in the target container to attach a terminal to, and a
 * distroless or scratch image has none — which is exactly the kind of image
 * worth running in production and therefore exactly the pod an operator most
 * needs to get into. This is `kubectl debug`'s trick: a second, throwaway
 * container written onto the running pod, sharing the chosen container's
 * process namespace, that the console execs into instead.
 *
 * It is a write like any other here — impersonated, answered by the cluster's
 * own RBAC, audited — but it is not a small one, and both things that make it
 * so are stated before the button rather than found out afterwards: the
 * container cannot be removed once it exists, and it shares the target's
 * namespaces for as long as the pod lives.
 *
 * The write landing is not the same moment as the container being attachable.
 * The image still has to be pulled and the container still has to start, and
 * an exec attempted before either finishes fails with an opaque "waiting to
 * start" that names neither. So this sheet does not hand control back the
 * instant the write succeeds — it polls the pod's own status until the debug
 * container reports running, and surfaces the reason in plain text if it
 * cannot: no terminal opens on a container that is not there yet to hold one.
 */

const DEBUG_BLURB =
  'This adds a second, throwaway container to the pod, sharing the chosen container’s process ' +
  'namespace so a shell can attach where the application image has none of its own. It goes down ' +
  'the same impersonated tunnel as every other write here — the cluster’s own RBAC decides ' +
  'whether it lands.'

/** The two things this cannot undo. Said before the click, not in the result. */
const DEBUG_LIMITS =
  'The container cannot be removed once it is added — the API server has no delete for an ' +
  'ephemeral container — and it shares the target container’s process namespace, and from there ' +
  'its network, for as long as the pod lives.'

/** How often the pod is re-read while waiting for the debug container to
    report running. Fast enough to feel live, slow enough that several
    operators debugging at once is not several extra reads a second. */
const POLL_INTERVAL_MS = 1500

export function DebugContainerSheet({
  cluster,
  pod,
  onClose,
  onStarted,
}: {
  cluster: Cluster
  pod: Pod
  onClose: () => void
  /** Called once the container is confirmed running and the exec half can
      begin — the caller retargets its terminal at `result.container`, never
      at the pod's own container the debug session was asked against. */
  onStarted: (result: DebugContainerResult) => void
}) {
  const [target, setTarget] = useState(pod.containers[0]?.name ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // Set once the write succeeds. Its presence is what switches the sheet from
  // asking a question to reporting on one already answered — the write itself
  // cannot be retried from here, only watched.
  const [starting, setStarting] = useState<DebugContainerResult | null>(null)
  const [waiting, setWaiting] = useState<{ reason?: string; message?: string } | null>(null)

  // Held in a ref for the same reason ShellTerminal holds `onEnded` in one: the
  // parent passes a fresh closure every render, and the poll loop below must
  // not restart from zero every time it does.
  const onStartedRef = useRef(onStarted)
  onStartedRef.current = onStarted

  async function run() {
    if (busy || !target) return
    setBusy(true)
    setError(null)
    try {
      const result = await debugPodContainer(cluster.id, pod.name, pod.namespace, target)
      setStarting(result)
    } catch (err) {
      setError(errorMessage(err, `A debug container could not be added to ${pod.name}.`))
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    if (!starting) return
    let cancelled = false
    let timer: number | undefined

    async function poll() {
      try {
        const fresh = await fetchPod(cluster.id, pod.namespace, pod.name)
        if (cancelled) return
        const status = fresh.ephemeral_containers.find((entry) => entry.name === starting!.container)
        if (status?.running) {
          onStartedRef.current(starting!)
          return
        }
        setWaiting(status ? { reason: status.reason, message: status.message } : null)
      } catch {
        // A read that fails here is not the debug session failing — the next
        // tick tries again, the same way a live-read anywhere else in the
        // console does.
      }
      if (!cancelled) timer = window.setTimeout(poll, POLL_INTERVAL_MS)
    }

    timer = window.setTimeout(poll, 0)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [starting, cluster.id, pod.namespace, pod.name])

  return (
    <Sheet
      onClose={onClose}
      eyebrow={`${pod.namespace} / ${pod.name}`}
      title={starting ? `Starting ${starting.container}` : 'Start a debug session'}
      width="md"
      footer={
        starting ? (
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" onClick={run} disabled={busy || !target}>
              {busy ? (
                <Loader2 aria-hidden="true" className="size-4 animate-spin" />
              ) : (
                <Bug aria-hidden="true" className="size-4" />
              )}
              Start debug session
            </Button>
          </>
        )
      }
    >
      <div className="flex flex-col gap-3">
        {starting ? (
          // Plain text, not a spinner: the wait is a live read the same as any
          // other in this console, and the reason it has not resolved — an
          // image still pulling, or one that never will — is worth more than
          // an animation saying "something is happening".
          <Notice tone={waiting?.reason ? 'warn' : 'info'}>
            {waiting?.reason
              ? `${starting.container} is not running yet — ${waiting.reason}` +
                (waiting.message ? `: ${waiting.message}` : '')
              : `${starting.container} is starting on ${pod.name} — waiting for it to report running.`}
          </Notice>
        ) : (
          <>
            {error ? <Notice tone="error">{error}</Notice> : null}
            <p className="text-[13px] leading-relaxed text-muted">{DEBUG_BLURB}</p>
            <Notice tone="warn">{DEBUG_LIMITS}</Notice>
            {pod.containers.length > 1 ? (
              <label className="flex flex-col gap-1.5 text-[12px] text-muted">
                Share the process namespace of
                <Select
                  aria-label="Container to debug"
                  value={target}
                  onChange={(event) => setTarget(event.target.value)}
                >
                  {pod.containers.map((entry) => (
                    <option key={entry.name} value={entry.name}>
                      {entry.name}
                    </option>
                  ))}
                </Select>
              </label>
            ) : null}
          </>
        )}
      </div>
    </Sheet>
  )
}
