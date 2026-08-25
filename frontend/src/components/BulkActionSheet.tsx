import { useState } from 'react'
import { Check, Loader2, Pause, Play, RotateCcw, Trash2, X } from 'lucide-react'
import { deleteResourceObject, errorMessage, restartWorkload, suspendWorkload } from '../api/client'
import type { Cluster } from '../api/types'
import type { BulkActionName, SelectedRow } from '../lib/selection'
import { BULK_ACTION_LABEL } from '../lib/selection'
import { Button, Notice, Sheet } from './primitives'

/*
 * Running one action over a selection.
 *
 * The whole surface exists because of one asymmetry: deciding to delete eight
 * finished pods takes a second and doing it one row at a time takes eight
 * confirmations, of which the last six are dismissed without reading. So the
 * decision is confirmed once, against a list of exactly what it will touch, and
 * then carried out.
 *
 * What it is *not* is a bulk API. There is no route that takes eight names:
 * each row is its own call down the same impersonated tunnel, its own line in
 * the audit trail and its own answer from the cluster — which is why this panel
 * reports per row rather than as one success or one failure. Four deleted, one
 * refused by RBAC and three still pending is the honest outcome of a mixed
 * selection, and no single response could carry it.
 *
 * The calls are sequential. A selection is small (it is what fits on a screen
 * and was ticked by hand), and the thing on the other end is somebody's API
 * server: eight parallel writes to save two seconds is the wrong trade for a
 * gateway whose whole point is that it is the calm path to the cluster.
 */

/** What happened to one row, as it happens. */
interface RowOutcome {
  state: 'pending' | 'running' | 'done' | 'failed'
  message?: string
}

const ACTION_ICON: Record<BulkActionName, typeof Trash2> = {
  delete: Trash2,
  restart: RotateCcw,
  suspend: Pause,
  resume: Play,
}

/**
 * What the action is, said once, in the operator's terms rather than the API's.
 * Delete gets the sentence about tense that the backend's own message carries:
 * a delete is a request for removal, and a pod with a grace period is still in
 * the list when it comes back.
 */
const ACTION_BLURB: Record<BulkActionName, string> = {
  delete:
    'Deleting asks the cluster to remove these objects, along with anything they own. ' +
    'Anything under a controller — a pod belonging to a Deployment, say — will be replaced by it.',
  restart:
    'A rollout restart replaces every pod these workloads own, one batch at a time. ' +
    'Nothing about the spec changes.',
  suspend: 'A suspended schedule stops firing. Runs already in flight are left alone.',
  resume: 'A resumed schedule fires again at its next matching time. Missed runs are not made up.',
}

export function BulkActionSheet({
  cluster,
  action,
  rows,
  onClose,
  onDone,
}: {
  cluster: Cluster
  action: BulkActionName
  rows: SelectedRow[]
  onClose: () => void
  /** Re-reads the list behind this sheet, which is now describing the past. */
  onDone?: () => Promise<void> | void
}) {
  const [outcomes, setOutcomes] = useState<Record<string, RowOutcome>>({})
  const [busy, setBusy] = useState(false)
  const [ran, setRan] = useState(false)

  const Icon = ACTION_ICON[action]
  const destructive = action === 'delete'
  const label = BULK_ACTION_LABEL[action]

  // One Kind, or several. "3 Pods" is worth saying; "5 objects" is what a mixed
  // selection honestly is, and inventing a plural for it would be worse.
  const kinds = new Set(rows.map((row) => row.label))
  const subject =
    kinds.size === 1
      ? `${rows.length} ${[...kinds][0]}${rows.length === 1 ? '' : 's'}`
      : `${rows.length} objects`

  async function run() {
    if (busy) return
    setBusy(true)
    setRan(true)
    setOutcomes(Object.fromEntries(rows.map((row) => [row.key, { state: 'pending' as const }])))

    for (const row of rows) {
      setOutcomes((current) => ({ ...current, [row.key]: { state: 'running' } }))
      try {
        const result = await runOne(cluster.id, action, row)
        setOutcomes((current) => ({
          ...current,
          [row.key]: { state: 'done', message: result },
        }))
      } catch (err) {
        // The cluster's own words. A refusal here is almost always its RBAC
        // answering — the call is impersonated like every other — and
        // translating that into "something went wrong" would hide the one
        // sentence that says what to do about it.
        setOutcomes((current) => ({
          ...current,
          [row.key]: { state: 'failed', message: errorMessage(err, 'The cluster refused this.') },
        }))
      }
    }

    setBusy(false)
    // Refreshed once, at the end: a list re-read after every row would redraw
    // the table under the panel eight times.
    await onDone?.()
  }

  const failed = rows.filter((row) => outcomes[row.key]?.state === 'failed').length
  const done = rows.filter((row) => outcomes[row.key]?.state === 'done').length

  return (
    <Sheet
      width="lg"
      eyebrow={cluster.name}
      title={`${label} ${subject}`}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            {ran && !busy ? 'Close' : 'Cancel'}
          </Button>
          {ran ? null : (
            <Button
              type="button"
              variant={destructive ? 'danger' : 'primary'}
              onClick={() => void run()}
              disabled={busy || rows.length === 0}
            >
              <Icon aria-hidden="true" className="size-4" />
              {label} {subject}
            </Button>
          )}
        </>
      }
    >
      <Notice tone={destructive ? 'warn' : 'info'}>{ACTION_BLURB[action]}</Notice>

      {/* Said before the click rather than after a refusal: the grant that
          decides this is the cluster's, not KubeMG's, and a selection can be
          half-permitted. */}
      {ran ? null : (
        <p className="text-[12.5px] leading-relaxed text-muted">
          Each of these is a separate call, made as you and recorded in the audit trail. The
          cluster&apos;s own RBAC decides each one, so some may be refused while others go through.
        </p>
      )}

      {ran && !busy ? (
        <Notice tone={failed > 0 ? (done > 0 ? 'warn' : 'error') : 'ok'}>
          {done} of {rows.length} went through
          {failed > 0 ? `, ${failed} refused` : ''}.
        </Notice>
      ) : null}

      <ul className="flex flex-col divide-y divide-line-soft rounded-card border border-line-soft">
        {rows.map((row) => {
          const outcome = outcomes[row.key]
          return (
            <li key={row.key} className="flex items-start gap-2.5 px-3 py-2">
              <OutcomeGlyph state={outcome?.state} />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-[12.5px] text-fg">{row.name}</span>
                <span className="block truncate text-[11.5px] text-faint">
                  {row.label}
                  {row.namespace ? ` · ${row.namespace}` : ''}
                </span>
                {outcome?.message ? (
                  <span
                    className={`mt-0.5 block text-[11.5px] leading-relaxed ${
                      outcome.state === 'failed' ? 'text-danger' : 'text-muted'
                    }`}
                  >
                    {outcome.message}
                  </span>
                ) : null}
              </span>
            </li>
          )
        })}
      </ul>
    </Sheet>
  )
}

/**
 * Where a row has got to. It is a glyph and not a colour alone, the same rule a
 * `Pill` follows: a running row and a refused one have to be told apart by
 * shape as well as by tone.
 */
function OutcomeGlyph({ state }: { state?: RowOutcome['state'] }) {
  switch (state) {
    case 'running':
      return <Loader2 aria-label="Running" className="mt-0.5 size-3.5 shrink-0 animate-spin text-accent" />
    case 'done':
      return <Check aria-label="Done" className="mt-0.5 size-3.5 shrink-0 text-ok" />
    case 'failed':
      return <X aria-label="Refused" className="mt-0.5 size-3.5 shrink-0 text-danger" />
    default:
      return (
        <span
          aria-hidden="true"
          className="mt-1.5 size-1.5 shrink-0 rounded-full bg-line"
        />
      )
  }
}

/**
 * One row, one call. The action names the route rather than the route taking
 * the action, so a kind that cannot answer for one never reaches here — the
 * selection decided that before the sheet opened.
 */
async function runOne(
  clusterId: number,
  action: BulkActionName,
  row: SelectedRow,
): Promise<string> {
  switch (action) {
    case 'delete': {
      const result = await deleteResourceObject(clusterId, row.kind, row.name, row.namespace)
      return result.message
    }
    case 'restart': {
      const result = await restartWorkload(clusterId, row.kind, row.name, row.namespace)
      return result.message
    }
    case 'suspend':
    case 'resume': {
      const result = await suspendWorkload(
        clusterId,
        row.kind,
        row.name,
        row.namespace,
        action === 'suspend',
      )
      return result.message
    }
  }
}
