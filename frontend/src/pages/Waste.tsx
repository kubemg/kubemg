import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { Cpu, HardDrive, MemoryStick, Network, Recycle, RefreshCw } from 'lucide-react'
import { errorMessage, fetchClusterWaste } from '../api/client'
import {
  WASTE_SPANS,
  type ClusterWaste,
  type WasteContainer,
  type WasteDimension,
  type WasteSpan,
  type WasteWorkload,
} from '../api/waste'
import { AppShell } from '../components/AppShell'
import {
  Age,
  Button,
  CodeBlock,
  Disclosure,
  EmptyState,
  Notice,
  Pill,
  Row,
  Segmented,
  Sheet,
  StatTile,
  Table,
  Td,
  Th,
} from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import { useDisclosureState } from '../lib/disclosures'
import { clusterPageHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { formatCPU, formatMemory } from '../lib/units'
import {
  BALANCER_STATE_LABEL,
  VOLUME_STATE_LABEL,
  rightsizingCommand,
  rightsizingPatch,
  sinceWindow,
  volumeExplanation,
} from '../lib/waste'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

/**
 * What a cluster reserves and does not use.
 *
 * Capacity says a node is mostly reserved and half idle; this says whose
 * reservation it is. Three readings, each answerable on its own:
 *
 *   - **workloads** whose containers request well past what they used over a
 *     window, sized from the cluster's metrics datasource — the one reading
 *     that needs history, and that says so when there is none;
 *   - **storage** no running pod mounts, and volumes left released or unbound;
 *   - **load balancers** with no ready endpoint behind them.
 *
 * **It recommends and never writes.** The change is shown as a patch for the
 * operator to apply where the manifest is kept; a button here would be undone
 * by the next sync of any workload a GitOps tool manages. **There is no money
 * on the page**: reclaimable capacity is cores and bytes, because a price is a
 * property of somebody's contract, not of the cluster.
 *
 * Not live: a 30-day history read is the most expensive question KubeMG asks a
 * metrics backend, so it is asked when the page opens or Refresh is pressed,
 * never on a tick.
 */

const WINDOW_KEY = 'kubemg.waste.window'

const SPAN_LABEL: Record<WasteSpan, string> = {
  '1d': '1 day',
  '7d': '7 days',
  '14d': '14 days',
  '30d': '30 days',
}

function storedSpan(): WasteSpan {
  try {
    const stored = window.localStorage.getItem(WINDOW_KEY)
    if (stored && (WASTE_SPANS as readonly string[]).includes(stored)) return stored as WasteSpan
  } catch {
    // A blocked store is a page that opens on the default.
  }
  return '7d'
}

function rememberSpan(span: WasteSpan) {
  try {
    window.localStorage.setItem(WINDOW_KEY, span)
  } catch {
    // Remembering the window is a convenience; the choice still applies now.
  }
}

/** Dash is "not declared" or "not measured", never zero — the title says which. */
function Reading({
  value,
  format,
  missing,
}: {
  value: number | undefined
  format: (value: number) => string
  missing: string
}) {
  if (value === undefined) {
    return (
      <span className="text-faint" title={missing}>
        —
      </span>
    )
  }
  return <span className="font-data tabular-nums">{format(value)}</span>
}

function DimensionCells({
  dimension,
  format,
  container,
}: {
  dimension: WasteDimension
  format: (value: number) => string
  container: WasteContainer
}) {
  return (
    <>
      <Td className="text-[12.5px]">
        <Reading value={dimension.request} format={format} missing="No request is declared" />
      </Td>
      <Td className="text-[12.5px] text-muted">
        <Reading
          value={dimension.observed}
          format={format}
          missing={container.note ?? 'Not measured'}
        />
      </Td>
      <Td className="text-[12.5px]">
        {dimension.over ? (
          <span className="font-data font-semibold text-fg tabular-nums">
            {format(dimension.recommended ?? 0)}
          </span>
        ) : (
          <span className="text-faint" title="Unchanged">
            keep
          </span>
        )}
      </Td>
    </>
  )
}

export function WorkloadSheet({
  workload,
  report,
  onClose,
}: {
  workload: WasteWorkload
  report: ClusterWaste
  onClose: () => void
}) {
  const patch = rightsizingPatch(workload)
  const notes = workload.containers.filter((container) => container.note)
  return (
    <Sheet
      title={workload.name}
      eyebrow={`${workload.kind} · ${workload.namespace}`}
      onClose={onClose}
      width="lg"
    >
      <div className="flex flex-col gap-5">
        <p className="text-[13px] leading-relaxed text-muted">
          {workload.replicas} {workload.replicas === 1 ? 'pod' : 'pods'} running. Usage is the
          95th percentile of CPU and the peak memory of the busiest replica
          {sinceWindow(workload, report.window.start) ? (
            <>
              {' '}
              since its history starts, <Age iso={workload.since} /> ago
            </>
          ) : (
            <> over the last {SPAN_LABEL[report.window.span] ?? report.window.span}</>
          )}
          ; a recommendation adds 20% on top.
        </p>

        {workload.rollout ? (
          <Notice tone="info">
            Its pods disagree about their requests, so a rollout is in progress. The newest pod’s
            request is shown, and older pods’ usage still counts.
          </Notice>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <Table>
            <thead>
              <tr>
                <Th className="w-[22%]">Container</Th>
                <Th>CPU request</Th>
                <Th>p95 used</Th>
                <Th>Suggested</Th>
                <Th>Memory request</Th>
                <Th>Peak used</Th>
                <Th>Suggested</Th>
              </tr>
            </thead>
            <tbody>
              {workload.containers.map((container) => (
                <Row key={container.name}>
                  <Td className="truncate">
                    <span className="font-data text-[13px] font-semibold text-fg">
                      {container.name}
                    </span>
                    {container.sidecar ? (
                      <span className="ml-1.5 text-[11.5px] text-faint">sidecar</span>
                    ) : null}
                  </Td>
                  <DimensionCells dimension={container.cpu} format={formatCPU} container={container} />
                  <DimensionCells
                    dimension={container.memory}
                    format={formatMemory}
                    container={container}
                  />
                </Row>
              ))}
            </tbody>
          </Table>
        </div>

        {notes.map((container) => (
          <p key={container.name} className="text-[12.5px] text-muted">
            <span className="font-data text-fg">{container.name}</span>: {container.note}
          </p>
        ))}

        {patch ? (
          <div className="flex flex-col gap-3">
            <CodeBlock label="Suggested change" value={patch} />
            <CodeBlock label="Apply it by hand" value={rightsizingCommand(workload)} />
            <p className="text-[12.5px] leading-relaxed text-muted">
              Only the requests found over-asked change; limits and everything else stay as the
              manifest has them. Apply it where the manifest is kept — if Argo CD, Flux or Helm
              manages this workload, a patch here is put back at the next sync. Nothing on this
              page writes to the cluster.
            </p>
          </div>
        ) : null}
      </div>
    </Sheet>
  )
}

/**
 * WorkloadsSection is the one reading that needs history. Without a metrics
 * datasource it says so and points at where one is registered, rather than
 * showing an empty list that reads as a clean cluster.
 */
export function WorkloadsSection({
  report,
  span,
  busy,
  isAdmin,
  clusterId,
  onOpen,
}: {
  report: ClusterWaste | null
  span: WasteSpan
  busy: boolean
  isAdmin: boolean
  clusterId: number
  onOpen: (workload: WasteWorkload) => void
}) {
  const sized = report?.workloads_available ?? false
  return (
    <section className="card min-w-0 overflow-hidden" aria-labelledby="waste-workloads">
      <div className="border-b border-line-soft px-5 pt-4 pb-3.5">
        <h2 id="waste-workloads" className="text-[16px] font-bold text-fg">
          Over-requested workloads
        </h2>
        <p className="mt-0.5 text-[13px] text-muted">
          Containers that ask for more than one and a half times what they used over the last{' '}
          {SPAN_LABEL[span]}
        </p>
      </div>

      {busy && !report ? <TableSkeleton columns={5} rows={4} label="Reading usage" /> : null}

      {report && !sized ? (
        <div className="p-5">
          <Notice tone="info">
            {report.workloads_reason}{' '}
            {isAdmin ? (
              <Link
                to={clusterPageHref(clusterId, 'dashboard')}
                className="text-accent hover:underline"
              >
                Register a metrics datasource
              </Link>
            ) : (
              'An administrator can register a metrics datasource for this cluster.'
            )}
          </Notice>
        </div>
      ) : null}

      {report && sized && report.workloads.length === 0 ? (
        <p className="px-5 py-10 text-center text-[13px] text-muted">
          Nothing asks for much more than it uses. Checked {report.summary.workloads_checked ?? 0}{' '}
          workloads
          {report.summary.workloads_short_history
            ? `; ${report.summary.workloads_short_history} have less than a day of history and were not sized`
            : ''}
          {report.summary.workloads_unmeasured
            ? `; ${report.summary.workloads_unmeasured} had no usage recorded`
            : ''}
          .
        </p>
      ) : null}

      {report && sized && report.workloads.length > 0 ? (
        <>
          <Table>
            <thead>
              <tr>
                <Th className="w-[34%]">Workload</Th>
                <Th className="hidden md:table-cell">Namespace</Th>
                <Th className="w-[12%]">Pods</Th>
                <Th className="w-[16%]">CPU to give back</Th>
                <Th className="w-[16%]">Memory to give back</Th>
              </tr>
            </thead>
            <tbody>
              {report.workloads.map((workload) => (
                <Row
                  key={`${workload.namespace}/${workload.kind}/${workload.name}`}
                  onOpen={() => onOpen(workload)}
                >
                  <Td className="truncate">
                    <button
                      type="button"
                      className="flex min-w-0 flex-col items-start gap-0.5 text-left"
                      onClick={() => onOpen(workload)}
                    >
                      <span className="truncate font-data text-[13.5px] font-semibold text-fg transition-colors group-hover/row:text-accent">
                        {workload.name}
                      </span>
                      <span className="text-[11.5px] text-faint">
                        {workload.kind}
                        {workload.rollout ? ' · rolling out' : ''}
                      </span>
                    </button>
                  </Td>
                  <Td className="hidden truncate font-data text-[12.5px] text-muted md:table-cell">
                    {workload.namespace}
                  </Td>
                  <Td className="font-data text-[12.5px] text-muted tabular-nums">
                    {workload.replicas}
                  </Td>
                  <Td className="text-[12.5px]">
                    {workload.reclaimable.cpu > 0 ? (
                      <span className="font-data tabular-nums">
                        {formatCPU(workload.reclaimable.cpu)}
                      </span>
                    ) : (
                      <span className="text-faint">—</span>
                    )}
                  </Td>
                  <Td className="text-[12.5px]">
                    {workload.reclaimable.memory > 0 ? (
                      <span className="font-data tabular-nums">
                        {formatMemory(workload.reclaimable.memory)}
                      </span>
                    ) : (
                      <span className="text-faint">—</span>
                    )}
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>
          {report.workloads_total > report.workloads.length ? (
            <p className="border-t border-line-soft px-5 py-3 text-[12.5px] text-muted">
              Showing the {report.workloads.length} that give back the most of{' '}
              {report.workloads_total}.
            </p>
          ) : null}
        </>
      ) : null}
    </section>
  )
}

export function Waste() {
  const params = useParams<{ id: string }>()
  const { user } = useAuth()
  const { clusters, loading: clustersLoading } = useClusters()
  const clusterId = Number(params.id)
  const [span, setSpan] = useState<WasteSpan>(storedSpan)
  const [open, setOpen] = useState<WasteWorkload | null>(null)
  const [methodologyOpen, setMethodologyOpen] = useDisclosureState(
    'waste.methodology',
    user?.id ?? null,
  )

  const cluster =
    clusters.find(
      (entry) =>
        entry.id === clusterId && entry.connection_mode === 'agent' && entry.agent_attached,
    ) ?? null
  const unreachable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  const report = useCachedQuery(cluster ? queryKey('waste', cluster.id, span) : null, async () => {
    if (!cluster) throw new Error('no cluster is selected')
    return fetchClusterWaste(cluster.id, span)
  })

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Waste">
        <div className="card">
          <EmptyState
            icon={<Recycle aria-hidden="true" className="size-5" />}
            title={
              unreachable
                ? `${unreachable.name} has no live connection`
                : 'That cluster is not registered'
            }
          >
            {unreachable ? (
              <>
                What a cluster reserves is read through the agent tunnel, so a cluster without one
                has nothing to show.{' '}
                <Link
                  to={clusterPageHref(unreachable.id, 'dashboard')}
                  className="text-accent hover:underline"
                >
                  Open the cluster
                </Link>{' '}
                to check its connection.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read what it leaves unused.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const loaded = report.data
  const busy = report.loading || report.revalidating
  const summary = loaded?.summary
  const sized = loaded?.workloads_available ?? false

  return (
    <AppShell
      title="Waste"
      fullWidth
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <Segmented
            ariaLabel="Usage window"
            value={span}
            onChange={(next) => {
              setSpan(next)
              rememberSpan(next)
            }}
            options={WASTE_SPANS.map((value) => ({ value, label: value }))}
          />
          <Button onClick={() => void report.refresh()} disabled={busy}>
            <RefreshCw aria-hidden="true" className={`size-4 ${busy ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </div>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {report.error ? (
          <Notice tone="error">
            {errorMessage(report.error, 'Could not read what this cluster leaves unused.')}
          </Notice>
        ) : null}
        {loaded?.truncated || loaded?.usage_truncated ? (
          <Notice tone="warn">
            This cluster is larger than one read collects, so the report covers part of it. A
            namespace-scoped view reads less at once.
          </Notice>
        ) : null}

        {summary ? (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatTile
              icon={Cpu}
              label="CPU to give back"
              value={sized ? formatCPU(summary.reclaimable_cpu) : '—'}
              dim={!sized}
              sub={sized ? `across ${loaded?.workloads_total ?? 0} workloads` : 'needs usage history'}
            />
            <StatTile
              icon={MemoryStick}
              label="Memory to give back"
              value={sized ? formatMemory(summary.reclaimable_memory) : '—'}
              dim={!sized}
              sub={sized ? `over the last ${SPAN_LABEL[span]}` : 'needs usage history'}
            />
            <StatTile
              icon={HardDrive}
              label="Storage nothing uses"
              value={formatMemory(summary.unused_storage)}
              sub={`${loaded?.volumes.length ?? 0} volumes`}
            />
            <StatTile
              icon={Network}
              label="Idle load balancers"
              value={loaded?.load_balancers_available ? summary.idle_load_balancers : '—'}
              dim={!loaded?.load_balancers_available}
              tone={summary.idle_load_balancers > 0 ? 'warn' : 'neutral'}
            />
          </div>
        ) : null}

        <WorkloadsSection
          report={loaded ?? null}
          span={span}
          busy={busy}
          isAdmin={user?.role === 'admin'}
          clusterId={clusterId}
          onOpen={setOpen}
        />

        <section className="card min-w-0 overflow-hidden" aria-labelledby="waste-volumes">
          <div className="border-b border-line-soft px-5 pt-4 pb-3.5">
            <h2 id="waste-volumes" className="text-[16px] font-bold text-fg">
              Storage nothing uses
            </h2>
            <p className="mt-0.5 text-[13px] text-muted">
              Claims no running pod mounts, and volumes left released or never bound
            </p>
          </div>
          {loaded && !loaded.volumes_pv_available && loaded.volumes_pv_reason ? (
            <div className="px-5 pt-4">
              <Notice tone="info">{loaded.volumes_pv_reason}</Notice>
            </div>
          ) : null}
          {loaded && loaded.volumes.length === 0 ? (
            <p className="px-5 py-10 text-center text-[13px] text-muted">
              Every claim is mounted, and no volume is waiting unbound.
            </p>
          ) : null}
          {loaded && loaded.volumes.length > 0 ? (
            <Table>
              <thead>
                <tr>
                  <Th className="w-[26%]">Name</Th>
                  <Th className="w-[14%]">State</Th>
                  <Th className="w-[10%]">Size</Th>
                  <Th className="hidden lg:table-cell w-[14%]">Storage class</Th>
                  <Th>Why it is here</Th>
                </tr>
              </thead>
              <tbody>
                {loaded.volumes.map((volume) => (
                  <Row key={`${volume.kind}/${volume.namespace ?? ''}/${volume.name}`}>
                    <Td className="truncate">
                      <span className="flex min-w-0 flex-col gap-0.5">
                        <span className="truncate font-data text-[13px] font-semibold text-fg">
                          {volume.name}
                        </span>
                        <span className="truncate text-[11.5px] text-faint">
                          {volume.kind === 'PersistentVolume'
                            ? 'PersistentVolume'
                            : `PersistentVolumeClaim · ${volume.namespace}`}
                        </span>
                      </span>
                    </Td>
                    <Td>
                      <Pill tone={volume.state === 'unmounted' ? 'idle' : 'warn'}>
                        {VOLUME_STATE_LABEL[volume.state]}
                      </Pill>
                    </Td>
                    <Td className="font-data text-[12.5px] tabular-nums">
                      {volume.capacity > 0 ? formatMemory(volume.capacity) : '—'}
                    </Td>
                    <Td className="hidden truncate font-data text-[12.5px] text-muted lg:table-cell">
                      {volume.storage_class || '—'}
                    </Td>
                    <Td className="text-[12.5px] text-muted">{volumeExplanation(volume)}</Td>
                  </Row>
                ))}
              </tbody>
            </Table>
          ) : null}
        </section>

        <section className="card min-w-0 overflow-hidden" aria-labelledby="waste-balancers">
          <div className="border-b border-line-soft px-5 pt-4 pb-3.5">
            <h2 id="waste-balancers" className="text-[16px] font-bold text-fg">
              Idle load balancers
            </h2>
            <p className="mt-0.5 text-[13px] text-muted">
              LoadBalancer Services with no ready endpoint behind them
            </p>
          </div>
          {loaded && !loaded.load_balancers_available && loaded.load_balancers_reason ? (
            <div className="p-5">
              <Notice tone="info">{loaded.load_balancers_reason}</Notice>
            </div>
          ) : null}
          {loaded && loaded.load_balancers_available && loaded.load_balancers.length === 0 ? (
            <p className="px-5 py-10 text-center text-[13px] text-muted">
              Every load balancer has something ready behind it.
            </p>
          ) : null}
          {loaded && loaded.load_balancers.length > 0 ? (
            <Table>
              <thead>
                <tr>
                  <Th className="w-[28%]">Service</Th>
                  <Th className="w-[18%]">State</Th>
                  <Th>Addresses</Th>
                  <Th className="w-[12%]">Age</Th>
                </tr>
              </thead>
              <tbody>
                {loaded.load_balancers.map((balancer) => (
                  <Row key={`${balancer.namespace}/${balancer.name}`}>
                    <Td className="truncate">
                      <span className="flex min-w-0 flex-col gap-0.5">
                        <span className="truncate font-data text-[13px] font-semibold text-fg">
                          {balancer.name}
                        </span>
                        <span className="truncate font-data text-[11.5px] text-faint">
                          {balancer.namespace}
                        </span>
                      </span>
                    </Td>
                    <Td>
                      <Pill tone="warn">{BALANCER_STATE_LABEL[balancer.state]}</Pill>
                    </Td>
                    <Td className="truncate font-data text-[12.5px] text-muted">
                      {balancer.addresses.length > 0
                        ? balancer.addresses.join(', ')
                        : 'no address assigned'}
                    </Td>
                    <Td className="text-[12.5px] text-muted">
                      <Age iso={balancer.created_at} />
                    </Td>
                  </Row>
                ))}
              </tbody>
            </Table>
          ) : null}
        </section>

        <Disclosure
          open={methodologyOpen}
          onOpenChange={setMethodologyOpen}
          summary="Where these numbers come from"
        >
          <div className="flex flex-col gap-3">
            <p className="text-[12px] leading-relaxed text-muted">
              Requests are read from the running pods and are exact. Usage is read from this
              cluster’s metrics datasource over the window: the 95th percentile of each container’s
              five-minute CPU rate, and its peak memory working set, taking the busiest replica. A
              request counts as over-asked when it is more than one and a half times that reading;
              the suggestion is the reading plus 20%, never below 10m of CPU or 32 MiB of memory,
              and a workload is listed only when it would give back at least 100m or 128 MiB.
              A workload with less than a day of history — counted from its oldest pod’s start or the
              datasource’s first sample of it, whichever is later — is not sized. Jobs, CronJobs and bare pods
              are left out. Nothing here estimates a cost or changes the cluster.
            </p>
            {loaded?.query ? (
              <>
                <CodeBlock label="CPU query" value={loaded.query.cpu} wrap />
                <CodeBlock label="Memory query" value={loaded.query.memory} wrap />
                <CodeBlock label="History query" value={loaded.query.history} wrap />
              </>
            ) : null}
          </div>
        </Disclosure>
      </div>

      {open && loaded ? (
        <WorkloadSheet workload={open} report={loaded} onClose={() => setOpen(null)} />
      ) : null}
    </AppShell>
  )
}
