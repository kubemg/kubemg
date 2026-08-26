import { useCallback, useEffect, useState } from 'react'
import { History, RefreshCw, Undo2, X } from 'lucide-react'
import { errorMessage, fetchHelmHistory, rollbackHelmRelease } from '../api/client'
import type { Cluster, HelmHistory, HelmRelease, HelmWriteResult } from '../api/types'
import type { Tone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { HelmObjectReport } from './HelmObjectReport'
import { Button, IconButton, Notice, Pill, Row, Table, Td, Th } from './primitives'

/**
 * What a release has been, and going back to one of those states.
 *
 * The Helm list shows one row per release because that answers what is
 * installed; every revision is still there, in a Secret of its own, and this is
 * the question the list cannot answer: what changed, when, and what an operator
 * would be going back to.
 *
 * **Roll back here is `helm rollback`.** The target revision's rendered
 * manifest is re-applied to the cluster — the current revision's manifest is
 * what is diffed away, the same three-way merge an upgrade performs — and the
 * result is recorded as a new revision carrying the target's chart, values and
 * manifest, exactly as Helm's own rollback records it. `HelmObjectReport` below
 * the confirmation is the same per-object report an install or an upgrade
 * answers with, because this write goes down the same tunnel one object at a
 * time.
 *
 * The confirmation is a panel above the table rather than an overlay over it,
 * for the reason the workload actions are: nothing in the console opens over
 * anything else, and the rows are what the choice is being made from.
 */
export function HelmHistoryPanel({
  cluster,
  release,
  onApplied,
}: {
  cluster: Cluster
  release: HelmRelease
  /** Refreshes the list behind the drawer once a revision has been written. */
  onApplied?: () => Promise<void> | void
}) {
  const [history, setHistory] = useState<HelmHistory | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // The revision a rollback has been asked for but not yet confirmed.
  const [pending, setPending] = useState<HelmRelease | null>(null)
  const [busy, setBusy] = useState(false)
  const [written, setWritten] = useState<HelmWriteResult | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await fetchHelmHistory(cluster.id, release.name, release.namespace)
      setHistory(result)
      setError(null)
    } catch (err) {
      // Reading a revision means reading a Secret, and the built-in view role
      // excludes those — the cluster's own refusal is the useful message.
      setError(errorMessage(err, 'Could not read this release’s history from the cluster.'))
      setHistory(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, release.name, release.namespace])

  useEffect(() => {
    void load()
  }, [load])

  async function rollback(target: HelmRelease) {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const result = await rollbackHelmRelease(
        cluster.id,
        release.name,
        release.namespace,
        target.revision,
      )
      setWritten(result)
      setPending(null)
      if (result.applied !== false) {
        // The history has a new row in it now, and so does the list behind this.
        await load()
        await onApplied?.()
      }
    } catch (err) {
      setError(errorMessage(err, 'The cluster refused to write the rollback revision.'))
    } finally {
      setBusy(false)
    }
  }

  const current = history?.release ?? release
  const rows = history?.history ?? []

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Pill tone="idle" dot={false}>
          {current.chart_name || 'chart'}
          {current.chart_version ? ` ${current.chart_version}` : ''}
        </Pill>
        <Pill tone="idle" dot={false}>
          {rows.length || 1} {rows.length === 1 ? 'revision' : 'revisions'}
        </Pill>

        <div className="ml-auto">
          <Button type="button" size="sm" onClick={() => void load()} disabled={loading || busy}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            Reload
          </Button>
        </div>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {/* Present only for a release that does not carry its chart, where a
          rollback can restore values and nothing more. Every other release
          rolls back for real, and says nothing here. */}
      {history?.warning ? <Notice tone="warn">{history.warning}</Notice> : null}
      {written ? <HelmObjectReport result={written} /> : null}

      {pending ? (
        <div className="flex flex-col gap-3 rounded-card border border-accent/40 bg-accent-soft/40 p-3">
          <div className="flex items-start gap-2">
            <h3 className="min-w-0 flex-1 text-[13.5px] font-semibold text-fg">
              Roll back to revision {pending.revision}
            </h3>
            <button
              type="button"
              onClick={() => setPending(null)}
              className="grid size-6 shrink-0 place-items-center rounded-control text-muted transition-colors hover:bg-raised hover:text-fg"
            >
              <X aria-hidden="true" className="size-3.5" />
              <span className="sr-only">Cancel</span>
            </button>
          </div>

          <p className="text-[13px] leading-relaxed text-muted">
            Revision {pending.revision}’s chart and values will be re-applied to the cluster and
            recorded as revision {current.revision + 1}, through the agent tunnel under your own
            identity. The cluster’s RBAC decides whether you may, and every object it touches is its
            own row in the audit trail.
          </p>

          <div className="flex items-center justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setPending(null)} disabled={busy}>
              Cancel
            </Button>
            <Button
              type="button"
              variant="primary"
              onClick={() => void rollback(pending)}
              disabled={busy}
            >
              <Undo2 aria-hidden="true" className="size-4" />
              {busy ? 'Rolling back…' : `Roll back to revision ${pending.revision}`}
            </Button>
          </div>
        </div>
      ) : null}

      {loading && !history ? (
        <p className="text-[13px] text-muted">Reading the release history…</p>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th className="w-[8rem]">Revision</Th>
              <Th className="w-[8rem]">Status</Th>
              <Th className="hidden w-[10rem] md:table-cell">Chart</Th>
              <Th className="w-[7rem]">Updated</Th>
              <Th>Description</Th>
              {/* 32px per button, 2px of gap, 32px of cell padding — the actions
                  column asks for what it needs, because `table-fixed` gives a
                  column exactly the width it asked for and no more. */}
              <Th className="w-[64px]" align="right">
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((entry) => {
              const isCurrent = entry.revision === current.revision
              return (
                <Row key={entry.revision}>
                  <Td className="font-mono">
                    {entry.revision}
                    {isCurrent ? (
                      <span className="ml-2 text-[12px] text-muted">current</span>
                    ) : null}
                  </Td>
                  <Td>
                    <Pill tone={revisionTone(entry.status)}>{entry.status || 'unknown'}</Pill>
                  </Td>
                  <Td className="hidden truncate font-mono md:table-cell" title={entry.chart_version}>
                    {entry.chart_version || '—'}
                  </Td>
                  <Td className="font-mono text-muted" title={entry.updated_at}>
                    {entry.updated_at ? relativeAge(entry.updated_at) : '—'}
                  </Td>
                  <Td className="truncate text-muted" title={entry.description}>
                    {entry.description || '—'}
                  </Td>
                  <Td className="text-right">
                    {isCurrent ? null : (
                      <IconButton
                        type="button"
                        label={`Roll back to revision ${entry.revision}`}
                        onClick={() => setPending(entry)}
                        disabled={busy}
                      >
                        <Undo2 aria-hidden="true" className="size-4" />
                      </IconButton>
                    )}
                  </Td>
                </Row>
              )
            })}
          </tbody>
        </Table>
      )}

      <p className="flex items-center gap-1.5 text-[12px] text-muted">
        <History aria-hidden="true" className="size-3.5" />
        Every revision is a Secret Helm wrote in this namespace; nothing here is stored by kubemg.
      </p>
    </>
  )
}

/**
 * Helm's own words for how a revision ended, mapped onto the deck's state tones.
 * An unrecognised status reads as idle rather than as a failure — a Helm version
 * this does not know is not a broken release.
 */
function revisionTone(status: string): Tone {
  switch (status) {
    case 'deployed':
      return 'ok'
    case 'superseded':
      return 'idle'
    case 'failed':
      return 'bad'
    case 'pending-install':
    case 'pending-upgrade':
    case 'pending-rollback':
    case 'uninstalling':
      return 'warn'
    default:
      return 'idle'
  }
}
