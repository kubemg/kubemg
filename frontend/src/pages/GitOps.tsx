import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { CircleAlert, GitCompareArrows, HeartPulse, Layers, RefreshCw } from 'lucide-react'
import { errorMessage, fetchGitopsState } from '../api/client'
import type { GitopsRecord, GitopsResource } from '../api/gitops'
import { AppShell } from '../components/AppShell'
import { ArgoConsoleLink } from '../components/ArgoConsoleLink'
import { ResourceDetailDrawer } from '../components/ResourceDetailDrawer'
import type { DetailTarget } from '../components/ResourceDetailDrawer'
import {
  Age,
  Button,
  EmptyState,
  Notice,
  Pill,
  Row,
  Sheet,
  StatTile,
  Table,
  Td,
  Th,
} from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import {
  HEALTH_LABEL,
  SYNC_LABEL,
  TOOL_LABEL,
  autoHealSentence,
  driftedResources,
  healthTone,
  hiddenNote,
  perObjectNote,
  resourceKeyFor,
  sourceLine,
  syncElsewhereNote,
  syncTone,
} from '../lib/gitops'
import { clusterPageHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { resourceItem, resourceSingular } from '../lib/resources'
import { useClusters } from '../state/clusters-context'
import { useInventory } from '../state/inventory-context'

/**
 * Which objects have drifted from what Argo CD or Flux declares.
 *
 * **Read from the controllers, never from Git.** Argo CD has already rendered
 * the repository, compared it against the cluster and written the answer per
 * resource; Flux has written which revision it applied. This page shows those
 * answers. It does not compute one of its own, so it never reports a
 * difference between two renders as drift.
 *
 * The records are read by KubeMG and narrowed by the server to the namespaces
 * the caller is granted, so a scoped developer sees drift in their own
 * namespaces even though Applications live in Argo CD's. What a record manages
 * outside the grant is counted, not shown.
 *
 * Not live: a sync state changes on the controller's clock. Refresh asks again.
 */

export function GitOps() {
  const params = useParams<{ id: string }>()
  const { clusters, loading: clustersLoading } = useClusters()
  const clusterId = Number(params.id)
  const [open, setOpen] = useState<GitopsRecord | null>(null)
  const [detail, setDetail] = useState<DetailTarget | null>(null)

  const cluster =
    clusters.find(
      (entry) =>
        entry.id === clusterId && entry.connection_mode === 'agent' && entry.agent_attached,
    ) ?? null
  const unreachable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  const state = useCachedQuery(cluster ? queryKey('gitops', cluster.id) : null, async () => {
    if (!cluster) throw new Error('no cluster is selected')
    return fetchGitopsState(cluster.id)
  })

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="GitOps">
        <div className="card">
          <EmptyState
            icon={<GitCompareArrows aria-hidden="true" className="size-5" />}
            title={
              unreachable
                ? `${unreachable.name} has no live connection`
                : 'That cluster is not registered'
            }
          >
            {unreachable ? (
              <>
                Sync state is read through the agent tunnel, so a cluster without one has nothing
                to show.{' '}
                <Link
                  to={clusterPageHref(unreachable.id, 'dashboard')}
                  className="text-accent hover:underline"
                >
                  Open the cluster
                </Link>{' '}
                to check its connection.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read its GitOps state.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const loaded = state.data
  const busy = state.loading || state.revalidating
  const installed = loaded?.tools.filter((tool) => tool.installed) ?? []
  const refused = installed.filter((tool) => !tool.available)

  return (
    <AppShell
      title="GitOps"
      fullWidth
      actions={
        <>
          <ArgoConsoleLink href={loaded?.argocd_console_url} label="Argo CD console" />
          <Button onClick={() => void state.refresh()} disabled={busy}>
            <RefreshCw aria-hidden="true" className={`size-4 ${busy ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {state.error ? (
          <Notice tone="error">{errorMessage(state.error, 'Could not read the GitOps records.')}</Notice>
        ) : null}
        {loaded?.truncated ? (
          <Notice tone="warn">
            This cluster holds more records than one read collects, so the list covers part of it.
          </Notice>
        ) : null}
        {refused.map((tool) => (
          <Notice key={tool.tool} tone="warn">
            {TOOL_LABEL[tool.tool]}: {tool.reason}
          </Notice>
        ))}

        {busy && !loaded ? (
          <div className="card min-w-0 overflow-hidden">
            <TableSkeleton columns={5} rows={4} label="Reading the GitOps records" />
          </div>
        ) : null}

        {loaded && installed.length === 0 ? (
          <div className="card">
            <EmptyState
              icon={<GitCompareArrows aria-hidden="true" className="size-5" />}
              title="Neither Argo CD nor Flux is installed in this cluster"
            >
              This page reads the sync state Argo CD and Flux record about the objects they manage.
              Where Argo CD runs in another cluster and deploys here, its records are on that
              cluster’s page.
            </EmptyState>
          </div>
        ) : null}

        {loaded && installed.length > 0 ? (
          <>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <StatTile
                icon={Layers}
                label="Records"
                value={loaded.summary.records}
                sub="Applications, Kustomizations and HelmReleases you can see"
              />
              <StatTile
                icon={GitCompareArrows}
                label="Out of sync"
                value={loaded.summary.out_of_sync}
                tone={loaded.summary.out_of_sync > 0 ? 'warn' : 'neutral'}
                sub="The cluster differs from what Git declares"
              />
              <StatTile
                icon={HeartPulse}
                label="Degraded"
                value={loaded.summary.degraded}
                tone={loaded.summary.degraded > 0 ? 'danger' : 'neutral'}
                sub="What they manage is broken or missing"
              />
            </div>

            <RecordsTable records={loaded.records} onOpen={setOpen} />
          </>
        ) : null}
      </div>

      {open && cluster ? (
        <RecordSheet
          record={open}
          onClose={() => setOpen(null)}
          onOpenObject={(target) => {
            setOpen(null)
            setDetail(target)
          }}
        />
      ) : null}
      {detail && cluster ? (
        <ResourceDetailDrawer
          key={`${detail.kind}/${detail.namespace ?? ''}/${detail.name}`}
          cluster={cluster}
          target={detail}
          onClose={() => setDetail(null)}
          onOpen={setDetail}
        />
      ) : null}
    </AppShell>
  )
}

export function RecordsTable({
  records,
  onOpen,
}: {
  records: GitopsRecord[]
  onOpen: (record: GitopsRecord) => void
}) {
  const linked = records.some((record) => record.console_url)
  return (
    <section className="card min-w-0 overflow-hidden" aria-labelledby="gitops-records">
      <div className="border-b border-line-soft px-5 pt-4 pb-3.5">
        <h2 id="gitops-records" className="text-[16px] font-bold text-fg">
          Records
        </h2>
        <p className="mt-0.5 text-[13px] text-muted">
          What needs a look comes first: out of sync, then broken
        </p>
      </div>
      {records.length === 0 ? (
        <p className="px-5 py-10 text-center text-[13px] text-muted">
          No record manages anything in the namespaces you can see.
        </p>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th className="w-[28%]">Name</Th>
              <Th className="w-[13%]">Sync</Th>
              <Th className="w-[13%]">Health</Th>
              <Th className="hidden lg:table-cell">Source</Th>
              <Th className="w-[12%]">Drifted</Th>
              {linked ? <Th className="w-[10%]">Argo CD</Th> : null}
            </tr>
          </thead>
          <tbody>
            {records.map((record) => {
              const drifted = driftedResources(record).length
              return (
                <Row key={`${record.tool}/${record.namespace}/${record.name}`} onOpen={() => onOpen(record)}>
                  <Td className="truncate">
                    <button
                      type="button"
                      className="flex min-w-0 flex-col items-start gap-0.5 text-left"
                      onClick={() => onOpen(record)}
                    >
                      <span className="truncate font-data text-[13.5px] font-semibold text-fg transition-colors group-hover/row:text-accent">
                        {record.name}
                      </span>
                      <span className="truncate text-[11.5px] text-faint">
                        {TOOL_LABEL[record.tool]} · <span className="font-data">{record.namespace}</span>
                        {record.remote ? ` · deploys to ${record.remote}` : ''}
                      </span>
                    </button>
                  </Td>
                  <Td>
                    <Pill tone={syncTone(record.sync)}>{SYNC_LABEL[record.sync]}</Pill>
                  </Td>
                  <Td>
                    <Pill tone={healthTone(record.health)}>{HEALTH_LABEL[record.health]}</Pill>
                  </Td>
                  <Td className="hidden truncate font-data text-[12.5px] text-muted lg:table-cell">
                    {sourceLine(record) || '—'}
                  </Td>
                  <Td className="font-data text-[12.5px] tabular-nums">
                    {record.tool === 'argocd' ? (
                      drifted
                    ) : (
                      <span className="text-faint" title={perObjectNote(record.tool) ?? undefined}>
                        —
                      </span>
                    )}
                  </Td>
                  {linked ? (
                    <Td>
                      <ArgoConsoleLink
                        href={record.console_url}
                        label="Open"
                        ariaLabel={`Open ${record.name} in Argo CD`}
                      />
                    </Td>
                  ) : null}
                </Row>
              )
            })}
          </tbody>
        </Table>
      )}
    </section>
  )
}

export function RecordSheet({
  record,
  onClose,
  onOpenObject,
}: {
  record: GitopsRecord
  onClose: () => void
  onOpenObject: (target: DetailTarget) => void
}) {
  const { discovered } = useInventory()
  const note = perObjectNote(record.tool)
  const hidden = hiddenNote(record)
  const elsewhere = syncElsewhereNote(record)

  function targetOf(resource: GitopsResource): DetailTarget | null {
    // Another cluster's objects are not this cluster's to open.
    if (record.remote) return null
    const key = resourceKeyFor(resource, discovered)
    if (!key) return null
    const item = resourceItem(key, discovered)
    return {
      kind: key,
      label: item ? resourceSingular(item) : resource.kind,
      name: resource.name,
      namespace: resource.namespace || undefined,
    }
  }

  return (
    <Sheet
      title={record.name}
      eyebrow={`${TOOL_LABEL[record.tool]} · ${record.namespace}`}
      onClose={onClose}
      width="lg"
    >
      <div className="flex flex-col gap-5">
        <div className="flex flex-wrap items-center gap-2">
          <Pill tone={syncTone(record.sync)}>{SYNC_LABEL[record.sync]}</Pill>
          <Pill tone={healthTone(record.health)}>{HEALTH_LABEL[record.health]}</Pill>
          {record.suspended ? <Pill tone="idle">Suspended</Pill> : null}
        </div>

        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-[13px]">
          {sourceLine(record) ? (
            <>
              <dt className="text-muted">Source</dt>
              <dd className="min-w-0 break-all font-data text-fg">{sourceLine(record)}</dd>
            </>
          ) : null}
          {record.project ? (
            <>
              <dt className="text-muted">Project</dt>
              <dd className="font-data text-fg">{record.project}</dd>
            </>
          ) : null}
          {record.revision ? (
            <>
              <dt className="text-muted">Applied</dt>
              <dd className="min-w-0 break-all font-data text-fg">{record.revision}</dd>
            </>
          ) : null}
          {record.attempted_revision ? (
            <>
              <dt className="text-muted">Attempted</dt>
              <dd className="min-w-0 break-all font-data text-warn">{record.attempted_revision}</dd>
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
          {record.remote ? (
            <>
              <dt className="text-muted">Deploys to</dt>
              <dd className="font-data text-fg">{record.remote}</dd>
            </>
          ) : null}
        </dl>

        <p className="text-[13px] leading-relaxed text-muted">{autoHealSentence(record)}</p>
        {elsewhere ? (
          <div className="flex flex-col items-start gap-1.5">
            <p className="text-[12.5px] text-muted">{elsewhere}</p>
            <ArgoConsoleLink href={record.console_url} />
          </div>
        ) : null}
        {record.message ? (
          <Notice tone="warn">
            <span className="inline-flex items-start gap-1.5">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              {record.message}
            </span>
          </Notice>
        ) : null}
        {note ? <p className="text-[12.5px] text-muted">{note}</p> : null}
        {hidden ? <p className="text-[12.5px] text-muted">{hidden}</p> : null}

        {record.resources.length > 0 ? (
          <div className="card min-w-0 overflow-hidden">
            <Table>
              <thead>
                <tr>
                  <Th className="w-[40%]">Object</Th>
                  <Th className="w-[20%]">Kind</Th>
                  <Th className="w-[20%]">Sync</Th>
                  <Th>Health</Th>
                </tr>
              </thead>
              <tbody>
                {record.resources.map((resource) => {
                  const target = targetOf(resource)
                  return (
                    <Row
                      key={`${resource.group}/${resource.kind}/${resource.namespace ?? ''}/${resource.name}`}
                      onOpen={target ? () => onOpenObject(target) : undefined}
                    >
                      <Td className="truncate">
                        <span className="flex min-w-0 flex-col gap-0.5">
                          {target ? (
                            <button
                              type="button"
                              className="truncate text-left font-data text-[13px] font-semibold text-fg hover:text-accent"
                              onClick={() => onOpenObject(target)}
                            >
                              {resource.name}
                            </button>
                          ) : (
                            <span className="truncate font-data text-[13px] font-semibold text-fg">
                              {resource.name}
                            </span>
                          )}
                          {resource.namespace ? (
                            <span className="truncate font-data text-[11.5px] text-faint">
                              {resource.namespace}
                            </span>
                          ) : null}
                        </span>
                      </Td>
                      <Td className="truncate text-[12.5px] text-muted">{resource.kind}</Td>
                      <Td>
                        <Pill tone={syncTone(resource.sync)}>{SYNC_LABEL[resource.sync]}</Pill>
                      </Td>
                      <Td>
                        {resource.health ? (
                          <Pill tone={healthTone(resource.health)}>{HEALTH_LABEL[resource.health]}</Pill>
                        ) : (
                          <span className="text-faint">—</span>
                        )}
                      </Td>
                    </Row>
                  )
                })}
              </tbody>
            </Table>
          </div>
        ) : null}
        {record.resources_truncated ? (
          <p className="text-[12.5px] text-muted">
            Only the first {record.resources.length} objects are listed; the drifted and unhealthy
            ones come first.
          </p>
        ) : null}
      </div>
    </Sheet>
  )
}
