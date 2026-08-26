import { useCallback, useEffect, useState } from 'react'
import { History, RefreshCw, Undo2, X } from 'lucide-react'
import { errorMessage, fetchWorkloadHistory, rollbackWorkload } from '../api/client'
import type { Cluster, WorkloadRevision } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { relativeAge } from '../lib/time'
import { Button, IconButton, Notice, Pill, Row, Table, Td, Th } from './primitives'

/**
 * A native workload's rollout history — `kubectl rollout history` and
 * `kubectl rollout undo`, read as the same surface the Helm release's History
 * tab is (`HelmHistoryPanel`), because the question is the same one: what has
 * this been, and what would going back to one of those states mean.
 *
 * The revisions themselves are already in the cluster — kube-controller-
 * manager keeps one ReplicaSet per Deployment revision and one
 * ControllerRevision per StatefulSet/DaemonSet revision — so nothing here is
 * stored by kubemg. **Rollback is the read-modify-write the scale/restart/
 * suspend actions already are**: the target revision's pod template is
 * written back onto the live object with its `resourceVersion`, so a
 * concurrent change becomes the API server's own 409 rather than a silent
 * overwrite, and the controller — not kubemg — performs the rollout from
 * there. Same impersonated tunnel, same audit record, a `view` grant refused
 * by the cluster's own RBAC exactly as every other write here is.
 *
 * The confirmation is a panel above the table rather than an overlay over it,
 * the same reason the workload actions and the Helm rollback are: nothing in
 * the console opens over anything else, and the rows are what the choice is
 * being made from.
 */
export function WorkloadHistoryPanel({
  cluster,
  kind,
  name,
  namespace,
  label,
  onApplied,
}: {
  cluster: Cluster
  kind: ResourceKey
  name: string
  namespace: string
  /** The singular Kind, for the confirmation's own words. */
  label: string
  /** Refreshes the object and the list behind the drawer once a revision has been written. */
  onApplied?: () => Promise<void> | void
}) {
  const [revisions, setRevisions] = useState<WorkloadRevision[]>([])
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // The revision a rollback has been asked for but not yet confirmed.
  const [pending, setPending] = useState<WorkloadRevision | null>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await fetchWorkloadHistory(cluster.id, kind, name, namespace)
      setRevisions(result.revisions ?? [])
      setTruncated(result.truncated ?? false)
      setError(null)
    } catch (err) {
      // A `view` grant is refused by the cluster's own RBAC here exactly as it
      // would be reading anything else — the cluster's own words are the
      // useful message.
      setError(errorMessage(err, `Could not read ${label.toLowerCase()}’s rollout history from the cluster.`))
      setRevisions([])
    } finally {
      setLoading(false)
    }
  }, [cluster.id, kind, name, namespace, label])

  useEffect(() => {
    void load()
  }, [load])

  async function rollback(target: WorkloadRevision) {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const result = await rollbackWorkload(cluster.id, kind, name, namespace, target.revision)
      setDone(result.message)
      setPending(null)
      // The history has a new current revision now, and so does the object
      // and list behind this.
      await load()
      await onApplied?.()
    } catch (err) {
      setError(errorMessage(err, 'The cluster refused to write the rollback.'))
    } finally {
      setBusy(false)
    }
  }

  const current = revisions.find((entry) => entry.current)

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Pill tone="idle" dot={false}>
          {revisions.length || 0} {revisions.length === 1 ? 'revision' : 'revisions'}
        </Pill>

        <div className="ml-auto">
          <Button type="button" size="sm" onClick={() => void load()} disabled={loading || busy}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            Reload
          </Button>
        </div>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {done ? <Notice tone="ok">{done}</Notice> : null}
      {truncated && !done ? (
        <Notice tone="info">Only the most recent revisions the cluster kept are listed here.</Notice>
      ) : null}

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
            Revision {pending.revision}’s pod template will be written back onto this{' '}
            {label.toLowerCase()}, through the agent tunnel under your own identity. The cluster’s RBAC
            decides whether you may, and the write is in the audit trail.
          </p>
          <Notice tone="warn">
            kubemg writes the pod template and stops there — the controller performs the rollout from
            it, exactly as <span className="font-mono">kubectl rollout undo</span> leaves it to.
          </Notice>

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
              {busy ? 'Writing…' : `Roll back to revision ${pending.revision}`}
            </Button>
          </div>
        </div>
      ) : null}

      {loading && revisions.length === 0 ? (
        <p className="text-[13px] text-muted">Reading the rollout history…</p>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th className="w-[7rem]">Revision</Th>
              <Th className="w-[8rem]">When</Th>
              <Th>Images</Th>
              <Th className="hidden md:table-cell">Change cause</Th>
              <Th className="w-[6rem]">Pods</Th>
              <Th className="w-[64px]" align="right">
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {revisions.map((entry) => {
              const isCurrent = entry.revision === current?.revision
              return (
                <Row key={entry.revision}>
                  <Td className="font-mono">
                    {entry.revision}
                    {isCurrent ? <span className="ml-2 text-[12px] text-muted">current</span> : null}
                  </Td>
                  <Td className="font-mono text-muted" title={entry.created_at}>
                    {entry.created_at ? relativeAge(entry.created_at) : '—'}
                  </Td>
                  <Td className="truncate font-mono text-[12.5px]" title={entry.images.join(', ')}>
                    {entry.images.length > 0 ? entry.images.join(', ') : '—'}
                  </Td>
                  <Td className="hidden truncate text-muted md:table-cell" title={entry.change_cause}>
                    {entry.change_cause || '—'}
                  </Td>
                  <Td className="font-mono text-muted">
                    {entry.ready != null && entry.replicas != null ? `${entry.ready}/${entry.replicas}` : '—'}
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
        Every revision is already in the cluster — a ReplicaSet or a ControllerRevision — and nothing
        here is stored by kubemg.
      </p>
    </>
  )
}
