import { useState } from 'react'
import { Link } from 'react-router'
import { GitCompareArrows } from 'lucide-react'
import { errorMessage, fetchGitopsObject } from '../api/client'
import type { Cluster } from '../api/types'
import {
  HEALTH_LABEL,
  SYNC_LABEL,
  TOOL_LABEL,
  autoHealSentence,
  healthTone,
  perObjectNote,
  sourceLine,
  syncElsewhereNote,
  syncTone,
} from '../lib/gitops'
import { clusterPageHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { ArgoConsoleLink } from './ArgoConsoleLink'
import { GitopsDiffSheet } from './GitopsDiffSheet'
import { Age, Button, Notice, Panel, Pill } from './primitives'

/*
 * Whether this object matches what Git declares, as Argo CD or Flux recorded
 * it. Drawn only for an object whose own metadata says a GitOps tool tracks it;
 * the record is found server-side from that metadata, read as KubeMG and
 * narrowed to the caller's grant.
 *
 * Read once, on open — not live. A sync state changes on the controller's
 * clock, and Refresh in the drawer is what asks again.
 *
 * Show diff is offered on an out-of-sync object an Argo CD record tracks, and
 * only when the cluster has an Argo CD datasource to ask; without one the
 * button is absent and the panel says why.
 */

export function GitopsObjectPanel({
  cluster,
  kind,
  name,
  namespace,
}: {
  cluster: Cluster
  kind: string
  name: string
  namespace?: string
}) {
  const state = useCachedQuery(queryKey('gitops-object', cluster.id, kind, namespace, name), () =>
    fetchGitopsObject(cluster.id, kind, name, namespace),
  )
  const view = state.data
  const record = view?.record ?? null
  const entry = view?.entry ?? null
  // The object's own line where the record lists it; the record's otherwise.
  const sync = entry?.sync ?? record?.sync
  const note = record ? perObjectNote(record.tool) : null
  const [diffOpen, setDiffOpen] = useState(false)
  const outOfSync = entry?.sync === 'out_of_sync'
  const offer = outOfSync ? view?.diff : undefined
  const elsewhere = record ? syncElsewhereNote(record) : null

  return (
    <Panel
      title="GitOps"
      eyebrow={record ? TOOL_LABEL[record.tool] : 'Sync state'}
      bodyClassName="flex flex-col gap-3 px-5 py-4"
    >
      {state.error ? (
        <Notice tone="warn">{errorMessage(state.error, 'Could not read the GitOps record.')}</Notice>
      ) : state.loading && !view ? (
        <p className="text-[12.5px] text-muted">Reading the GitOps record…</p>
      ) : !record ? (
        <p className="text-[12.5px] text-muted">{view?.reason ?? 'No GitOps record tracks this object.'}</p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {sync ? <Pill tone={syncTone(sync)}>{SYNC_LABEL[sync]}</Pill> : null}
            {entry?.health ? (
              <Pill tone={healthTone(entry.health)}>{HEALTH_LABEL[entry.health]}</Pill>
            ) : null}
            <span className="min-w-0 truncate text-[12.5px] text-muted">
              {entry ? 'This object' : 'The record'} ·{' '}
              <Link
                to={clusterPageHref(cluster.id, 'gitops')}
                className="font-data text-accent hover:underline"
              >
                {record.namespace}/{record.name}
              </Link>
            </span>
          </div>

          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-[12.5px]">
            {sourceLine(record) ? (
              <>
                <dt className="text-muted">Source</dt>
                <dd className="min-w-0 truncate font-data text-fg" title={sourceLine(record)}>
                  {sourceLine(record)}
                </dd>
              </>
            ) : null}
            {record.revision ? (
              <>
                <dt className="text-muted">Applied</dt>
                <dd className="min-w-0 truncate font-data text-fg">{record.revision}</dd>
              </>
            ) : null}
            {record.attempted_revision ? (
              <>
                <dt className="text-muted">Attempted</dt>
                <dd className="min-w-0 truncate font-data text-warn">{record.attempted_revision}</dd>
              </>
            ) : null}
            {record.reconciled_at ? (
              <>
                <dt className="text-muted">Compared</dt>
                <dd className="text-fg">
                  <Age iso={record.reconciled_at} /> ago
                </dd>
              </>
            ) : null}
          </dl>

          <p className="text-[12.5px] text-muted">{autoHealSentence(record)}</p>
          {elsewhere ? <p className="text-[12.5px] text-muted">{elsewhere}</p> : null}
          {note ? (
            <p className="text-[12.5px] text-muted">{note}</p>
          ) : !entry && view?.reason ? (
            <p className="text-[12.5px] text-muted">{view.reason}</p>
          ) : null}
          {record.message ? <Notice tone="warn">{record.message}</Notice> : null}
          {offer?.reason && !offer.available ? (
            <p className="text-[12.5px] text-muted">{offer.reason}</p>
          ) : null}
          {offer?.available || record.console_url ? (
            <div className="flex flex-wrap items-center gap-3">
              {offer?.available ? (
                <Button size="sm" onClick={() => setDiffOpen(true)}>
                  <GitCompareArrows aria-hidden="true" className="size-3.5" />
                  Show diff
                </Button>
              ) : null}
              <ArgoConsoleLink href={record.console_url} />
            </div>
          ) : null}
        </>
      )}
      {diffOpen ? (
        <GitopsDiffSheet
          cluster={cluster}
          kind={kind}
          name={name}
          namespace={namespace}
          consoleURL={record?.console_url}
          onClose={() => setDiffOpen(false)}
        />
      ) : null}
    </Panel>
  )
}
