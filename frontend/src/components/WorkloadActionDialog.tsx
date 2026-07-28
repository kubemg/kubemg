import { useState } from 'react'
import { RotateCcw, SlidersHorizontal } from 'lucide-react'
import { errorMessage, restartWorkload, scaleWorkload } from '../api/client'
import type { Cluster } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/*
 * Scale and rollout restart, as the two small surfaces they deserve to be.
 *
 * Both are already possible on the YAML tab, and that is the point: changing one
 * integer by hand-editing a thousand-line manifest is a poor way to do the most
 * common thing anyone does to a workload, and asking for a rollout by typing a
 * timestamp into an annotation is not something anyone should have to remember.
 *
 * Neither is a privilege the console did not already have. The write goes down
 * the same impersonated tunnel as every other call, so a `view` grant is refused
 * by the cluster's own RBAC and every action is in the audit trail — which is why
 * a failure here is shown in the cluster's own words rather than translated.
 */

export type WorkloadActionName = 'scale' | 'restart'

/** WorkloadActionTarget is one workload, and what is about to be asked of it. */
export interface WorkloadActionTarget {
  action: WorkloadActionName
  kind: ResourceKey
  /** The singular Kind, for the dialog's own words: "Scale this Deployment". */
  label: string
  name: string
  namespace?: string
  /** The replica count the list or the describe already read, for the prefill. */
  replicas?: number
}

/** The ceiling the backend enforces; repeated here so the field says so first. */
const MAX_REPLICAS = 1000

export function WorkloadActionDialog({
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
      // The list behind this dialog is now wrong; refreshing it is the whole
      // point of having acted from it.
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
    <Sheet
      width="md"
      eyebrow={`${cluster.name}${target.namespace ? ` · ${target.namespace}` : ''} · ${target.label}`}
      title={scaling ? 'Scale workload' : 'Rollout restart'}
      onClose={onClose}
      onSubmit={
        done
          ? undefined
          : (event) => {
              event.preventDefault()
              void run()
            }
      }
      footer={
        done ? (
          <Button type="button" onClick={onClose}>
            Close
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
        )
      }
    >
      <p className="text-[13px] text-muted">
        <span className="font-mono text-fg">{target.name}</span>
        {target.namespace ? (
          <>
            {' in '}
            <span className="font-mono text-fg">{target.namespace}</span>
          </>
        ) : null}
      </p>

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
    </Sheet>
  )
}
