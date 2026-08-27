import { useEffect, useState } from 'react'
import { RotateCcw, SlidersHorizontal, X } from 'lucide-react'
import {
  errorMessage,
  fetchWorkloadAutoscaler,
  restartWorkload,
  scaleWorkload,
} from '../api/client'
import type { Cluster } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { Button, Field, Notice, TextInput } from './primitives'

/*
 * Scale and rollout restart, as the two small surfaces they deserve to be.
 *
 * Both are already possible on the YAML tab, and that is the point: changing one
 * integer by hand-editing a thousand-line manifest is a poor way to do the most
 * common thing anyone does to a workload, and asking for a rollout by typing a
 * timestamp into an annotation is not something anyone should have to remember.
 *
 * This is a panel inside the detail drawer rather than a dialog over it. A
 * dialog stacked on the surface showing the object hid the very thing being
 * acted on — the conditions and events that are the reason anyone reaches for
 * Restart — behind the confirmation for acting on it, and it was the only thing
 * in the console that ever put one overlay on top of another.
 *
 * Neither action is a privilege the console did not already have. The write goes
 * down the same impersonated tunnel as every other call, so a `view` grant is
 * refused by the cluster's own RBAC and every action is in the audit trail —
 * which is why a failure here is shown in the cluster's own words rather than
 * translated.
 */

export type WorkloadActionName = 'scale' | 'restart'

/** WorkloadActionTarget is one workload, and what is about to be asked of it. */
export interface WorkloadActionTarget {
  action: WorkloadActionName
  kind: ResourceKey
  /** The singular Kind, for the panel's own words: "Scale this Deployment". */
  label: string
  name: string
  namespace?: string
  /** The replica count the list or the describe already read, for the prefill. */
  replicas?: number
}

/** The ceiling the backend enforces; repeated here so the field says so first. */
const MAX_REPLICAS = 1000

/**
 * The kinds an HorizontalPodAutoscaler can own. Asking about any other kind is
 * a mistake the backend names rather than an empty answer, so this panel only
 * asks where the question makes sense — a DaemonSet has no `scale` subresource
 * for an autoscaler to write.
 */
const AUTOSCALABLE: readonly ResourceKey[] = ['deployments', 'statefulsets', 'replicasets']

export function WorkloadActionPanel({
  cluster,
  target,
  onClose,
  onDone,
}: {
  cluster: Cluster
  target: WorkloadActionTarget
  onClose: () => void
  /** Refreshes whatever was showing the workload. */
  onDone?: () => Promise<void> | void
}) {
  const scaling = target.action === 'scale'

  const [replicas, setReplicas] = useState(String(target.replicas ?? 1))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)

  /*
   * Whether an autoscaler owns this number.
   *
   * Without it this control lies by omission: the write succeeds, reports the
   * count it set, and is reverted on the autoscaler's next pass with nothing
   * anywhere saying why. It is read when the panel opens rather than reported
   * after the write, because being told before is the difference between a
   * decision and a surprise — and it is deliberately not a *refusal*: forcing a
   * floor by hand while debugging is legitimate, and the manifest editor could
   * always do it anyway.
   *
   * A read that fails leaves the notice absent. It is context, not a
   * precondition, and failing to open the scale control because an optional
   * question could not be answered would be the worse trade.
   */
  const [autoscaler, setAutoscaler] = useState<string | null>(null)

  useEffect(() => {
    if (!scaling || !AUTOSCALABLE.includes(target.kind)) return
    let live = true
    void fetchWorkloadAutoscaler(cluster.id, target.kind, target.name, target.namespace)
      .then((answer) => {
        if (live) setAutoscaler(answer.notice ?? null)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [cluster.id, scaling, target.kind, target.name, target.namespace])

  const count = Number(replicas)
  const valid =
    replicas.trim() !== '' && Number.isInteger(count) && count >= 0 && count <= MAX_REPLICAS

  async function run() {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const result = scaling
        ? await scaleWorkload(cluster.id, target.kind, target.name, target.namespace, count)
        : await restartWorkload(cluster.id, target.kind, target.name, target.namespace)
      setDone(result.message)
      // The drawer behind this panel, and the list behind that, are now both
      // wrong; refreshing them is the whole point of having acted here.
      await onDone?.()
    } catch (err) {
      setError(
        errorMessage(
          err,
          scaling
            ? 'The cluster refused to scale this workload.'
            : 'The cluster refused to restart this workload.',
        ),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      className="flex flex-col gap-3 rounded-card border border-accent/40 bg-accent-soft/40 p-3"
      onSubmit={(event) => {
        event.preventDefault()
        if (!done) void run()
      }}
    >
      <div className="flex items-start gap-2">
        <h3 className="min-w-0 flex-1 text-[13.5px] font-semibold text-fg">
          {scaling ? 'Scale workload' : 'Rollout restart'}
        </h3>
        <button
          type="button"
          onClick={onClose}
          className="grid size-6 shrink-0 place-items-center rounded-control text-muted transition-colors hover:bg-raised hover:text-fg"
        >
          <X aria-hidden="true" className="size-3.5" />
          <span className="sr-only">Cancel</span>
        </button>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {done ? <Notice tone="ok">{done}</Notice> : null}

      {!done && scaling ? (
        <>
          <Field
            label="Replicas"
            htmlFor="workload-replicas"
            hint={
              target.replicas === undefined
                ? `Between 0 and ${MAX_REPLICAS}.`
                : `Currently ${target.replicas}. Between 0 and ${MAX_REPLICAS}.`
            }
            error={valid ? undefined : `Enter a whole number between 0 and ${MAX_REPLICAS}.`}
          >
            <TextInput
              id="workload-replicas"
              type="number"
              min={0}
              max={MAX_REPLICAS}
              step={1}
              autoFocus
              value={replicas}
              onChange={(event) => setReplicas(event.target.value)}
            />
          </Field>
          {autoscaler ? <Notice tone="warn">{autoscaler}</Notice> : null}
          {count === 0 && valid ? (
            <Notice tone="warn">
              Scaling to zero stops this workload: every pod is removed, and nothing takes their
              place until you scale it back up. The object itself stays, so its configuration is
              not lost.
            </Notice>
          ) : null}
        </>
      ) : null}

      {!done && !scaling ? (
        <>
          <p className="text-[13px] leading-relaxed text-muted">
            Every pod of this {target.label.toLowerCase()} will be replaced, following its own
            update strategy — the same thing <span className="font-mono">kubectl rollout restart</span>{' '}
            does. Nothing about its configuration changes: the workload is stamped with the time you
            asked, which is what makes the controller roll it.
          </p>
          <Notice tone="warn">
            This restarts pods that are serving traffic right now. A workload with one replica, or
            one whose strategy replaces pods before the new ones are ready, will be briefly
            unavailable.
          </Notice>
        </>
      ) : null}

      <div className="flex items-center justify-end gap-2">
        {done ? (
          <Button type="button" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy || (scaling && !valid)}>
              {scaling ? (
                <SlidersHorizontal aria-hidden="true" className="size-4" />
              ) : (
                <RotateCcw aria-hidden="true" className="size-4" />
              )}
              {busy ? 'Working…' : scaling ? 'Scale' : 'Restart'}
            </Button>
          </>
        )}
      </div>
    </form>
  )
}
