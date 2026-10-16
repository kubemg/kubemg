import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router'
import { ChevronDown, Cpu, Server } from 'lucide-react'
import { errorMessage, fetchClusterCapacity } from '../api/client'
import type {
  CapacityDimension,
  CapacitySeverity,
  NodeCapacity as NodeCapacityRow,
  PlacementSummary,
} from '../api/types'
import { AppShell } from '../components/AppShell'
import { CapacityHeatmap } from '../components/CapacityHeatmap'
import {
  AllocationLine,
  CapacityRing,
  FitBar,
  SegmentBar,
  ShareRow,
  SlotLine,
} from '../components/CapacityVisuals'
import { LiveRefresh } from '../components/LiveRefresh'
import { Disclosure, EmptyState, Notice, Pill } from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import {
  fitShare,
  nodeHasDetail,
  nodeStateSegments,
  percentText,
  qosSegments,
  relativeShare,
} from '../lib/capacityView'
import { useDisclosureState } from '../lib/disclosures'
import { queryKey, useCachedQuery } from '../lib/query'
import { formatCPU, formatMemory } from '../lib/units'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

/**
 * Allocation, which is the question the Capacity panel on the cluster page
 * cannot answer.
 *
 * That panel shows consumption, and consumption explains the least: a node at
 * 30% CPU can be one the scheduler will refuse to place another pod on, because
 * placement is decided on **requests** — a reservation nobody is obliged to
 * spend. The complaint this page answers is "there is plenty of room and
 * nothing will schedule", and no view built on usage alone can answer it.
 *
 * So every bar here carries three numbers against the same allocatable
 * denominator: what is reserved, what is actually being used, and what the
 * ceiling would be if every container spent its limit. Reserved is the fill,
 * because reserved is what decides scheduling; used is a tick on the same
 * track, because the gap between the two *is* the finding.
 *
 * **Limits are stated rather than drawn.** They routinely exceed a node's own
 * size — that is what overcommitment means — and a bar that runs off the end of
 * its track, or one silently clamped to it, would misreport by exactly the
 * amount that matters. A number that cannot be drawn honestly is written out.
 *
 * The verdicts are the server's, not this page's: it does the arithmetic and
 * writes the sentence, and the browser renders it. A claim about a cluster
 * assembled client-side is one that can drift from the numbers beside it.
 *
 * Live usage is the one column that can be missing, since metrics-server is
 * optional. The page is whole without it — reserved and limits come from the
 * pod specs — and says which column is absent instead of failing.
 */

const SEVERITY_TONE: Record<CapacitySeverity, 'ok' | 'warn' | 'bad' | 'idle'> = {
  ok: 'ok',
  note: 'idle',
  warn: 'warn',
  danger: 'bad',
}

const SEVERITY_LABEL: Record<CapacitySeverity, string> = {
  ok: 'nothing to flag',
  note: 'worth a look',
  warn: 'needs attention',
  danger: 'will not schedule',
}

/**
 * NodeRow is one node: its three ceilings as one line each, its concerns as
 * pills whose sentence is the tooltip, and the rest — every concern in full,
 * taints, the largest reservations and the pods using more than they reserved
 * — one click away. `focus` is the heatmap asking for this row: it opens and
 * is brought into view, with no smooth scroll.
 */
function NodeRow({
  node,
  focus,
  usageAvailable,
}: {
  node: NodeCapacityRow
  focus: number
  usageAvailable: boolean
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLLIElement>(null)
  const hasDetail = nodeHasDetail(node)

  useEffect(() => {
    if (focus === 0) return
    setOpen(true)
    ref.current?.scrollIntoView({ block: 'start' })
  }, [focus])

  return (
    <li
      ref={ref}
      id={`node-${node.name}`}
      className="defer-row min-w-0 scroll-mt-24 px-5 py-4 [contain-intrinsic-size:auto_140px]"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span
          aria-hidden="true"
          className={`size-2 shrink-0 rounded-full ${SEVERITY_DOT[node.severity]}`}
        />
        <span className="min-w-0 truncate font-data text-[13.5px] font-semibold text-fg">
          {node.name}
        </span>
        {node.roles.map((role) => (
          <span key={role} className="text-[12px] text-faint">
            {role}
          </span>
        ))}
        {!node.ready ? <Pill tone="bad">not ready</Pill> : null}
        {!node.schedulable ? <Pill tone="warn">cordoned</Pill> : null}
        {node.ready && node.schedulable && !node.placeable ? (
          <Pill tone="idle" title={node.taints.join(', ')}>
            tainted
          </Pill>
        ) : null}
        <span className="ml-auto">
          <Pill tone={SEVERITY_TONE[node.severity]}>{SEVERITY_LABEL[node.severity]}</Pill>
        </span>
      </div>

      <div className="mt-3 grid gap-x-8 gap-y-2.5 lg:grid-cols-[minmax(0,1fr)_minmax(0,220px)]">
        <div className="flex min-w-0 flex-col gap-2">
          <AllocationLine
            label="CPU"
            dimension={node.cpu}
            format={formatCPU}
            usageAvailable={usageAvailable}
          />
          <AllocationLine
            label="Memory"
            dimension={node.memory}
            format={formatMemory}
            usageAvailable={usageAvailable}
          />
          <SlotLine slots={node.pods} />
        </div>
        <div className="flex min-w-0 flex-col justify-center gap-2">
          <SegmentBar label="QoS" segments={qosSegments(node.qos)} compact />
          <p className="text-[11.5px] text-faint">
            Free{' '}
            {node.placeable ? (
              <span className="font-data text-fg tabular-nums">
                {formatCPU(node.headroom.cpu)} · {formatMemory(node.headroom.memory)}
              </span>
            ) : (
              'for no ordinary pod'
            )}
          </p>
        </div>
      </div>

      {node.concerns.length > 0 || hasDetail ? (
        <div className="mt-3 flex flex-wrap items-center gap-1.5">
          {node.concerns.map((concern) => (
            <Pill key={concern.code} tone={SEVERITY_TONE[concern.severity]} title={concern.detail}>
              {concern.title}
            </Pill>
          ))}
          {hasDetail ? (
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen((current) => !current)}
              className="ml-auto inline-flex items-center gap-1 text-[12px] text-accent hover:underline"
            >
              <ChevronDown
                aria-hidden="true"
                className={`size-3.5 transition-transform ${open ? 'rotate-180' : ''}`}
              />
              {open ? 'Less' : 'Details'}
            </button>
          ) : null}
        </div>
      ) : null}

      {open ? <NodeDetail node={node} /> : null}
    </li>
  )
}

const SEVERITY_DOT: Record<CapacitySeverity, string> = {
  ok: 'bg-ok',
  note: 'bg-faint',
  warn: 'bg-warn',
  danger: 'bg-danger',
}

/** NodeDetail is what a node's row folds: the sentences, then the pods behind the numbers. */
function NodeDetail({ node }: { node: NodeCapacityRow }) {
  const excess = node.top_borrowers.map((pod) => pod.excess_percent)
  const shares = node.top_requests.map((pod) => pod.share_percent)
  return (
    <div className="mt-3 grid gap-4 rounded-control border border-line-soft bg-sunken p-3 lg:grid-cols-2">
      {node.concerns.length > 0 || node.taints.length > 0 ? (
        <ul className="flex flex-col gap-1.5 text-[12.5px] leading-relaxed text-muted lg:col-span-2">
          {node.concerns.map((concern) => (
            <li key={concern.code}>
              <span className="font-medium text-fg">{concern.title}.</span> {concern.detail}
            </li>
          ))}
          {node.taints.length > 0 ? (
            <li>
              <span className="font-medium text-fg">Taints.</span>{' '}
              <span className="font-mono text-[12px]">{node.taints.join(', ')}</span>
              {node.placeable
                ? ' — none keeps an ordinary pod off.'
                : ' — only pods that tolerate them land here.'}
            </li>
          ) : null}
        </ul>
      ) : null}

      {node.top_requests.length > 0 ? (
        <div className="min-w-0">
          <p className="label mb-2 text-faint">Largest reservations</p>
          <ul className="flex flex-col gap-1.5">
            {node.top_requests.map((pod) => (
              <ShareRow
                key={`${pod.namespace}/${pod.name}`}
                namespace={pod.namespace}
                name={pod.name}
                share={relativeShare(pod.share_percent, shares)}
                figure={`${pod.share_percent.toFixed(0)}%`}
                // Only what the pod actually reserves: "250m · 0" would read
                // as a memory request of zero, not the absence of one.
                detail={[
                  pod.cpu_millicores > 0 ? formatCPU(pod.cpu_millicores) : null,
                  pod.memory_bytes > 0 ? formatMemory(pod.memory_bytes) : null,
                ]
                  .filter(Boolean)
                  .join(' · ')}
              />
            ))}
          </ul>
        </div>
      ) : null}

      {node.top_borrowers.length > 0 ? (
        <div className="min-w-0">
          {/* The noisy neighbours: under memory pressure what the kubelet
              evicts first, and on CPU what slows the pods beside them. */}
          <p className="label mb-2 text-faint">
            Using more than reserved
            {node.borrowing_pods > node.top_borrowers.length
              ? ` · top ${node.top_borrowers.length} of ${node.borrowing_pods}`
              : ''}
          </p>
          <ul className="flex flex-col gap-1.5">
            {node.top_borrowers.map((pod) => (
              <ShareRow
                key={`${pod.namespace}/${pod.name}`}
                namespace={pod.namespace}
                name={pod.name}
                share={relativeShare(pod.excess_percent, excess)}
                figure={`+${pod.excess_percent.toFixed(0)}%`}
                detail={[
                  pod.qos || null,
                  pod.cpu_used > pod.cpu_request
                    ? `${formatCPU(pod.cpu_used)} of ${pod.cpu_request > 0 ? formatCPU(pod.cpu_request) : 'none'} CPU`
                    : null,
                  pod.memory_used > pod.memory_request
                    ? `${formatMemory(pod.memory_used)} of ${pod.memory_request > 0 ? formatMemory(pod.memory_request) : 'none'} memory`
                    : null,
                  'taken beyond its request, as a share of the node',
                ]
                  .filter(Boolean)
                  .join(' · ')}
              />
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}

/** ClusterRing is one cluster-wide ceiling with its in-use and limit figures under it. */
function ClusterRing({
  label,
  dimension,
  format,
  usageAvailable,
}: {
  label: string
  dimension: CapacityDimension
  format: (value: number) => string
  usageAvailable: boolean
}) {
  const measured = dimension.allocatable > 0
  const used = usageAvailable && dimension.used > 0
  return (
    <CapacityRing
      label={label}
      reservedPercent={dimension.requested_percent}
      usedPercent={used ? dimension.used_percent : null}
      measured={measured}
      amount={
        measured
          ? `${format(dimension.requested)} / ${format(dimension.allocatable)}`
          : 'no allocatable'
      }
      title={
        measured
          ? `${label}: ${format(dimension.requested)} of ${format(dimension.allocatable)} reserved` +
            (used ? `, ${format(dimension.used)} in use` : ', live use not measured') +
            (dimension.limited > 0
              ? `, limits ${percentText(dimension.limited_percent)} of allocatable`
              : ', no limits declared')
          : `${label}: no allocatable capacity reported`
      }
      footer={
        measured ? (
          <>
            <span>
              <span aria-hidden="true" className="mr-1 inline-block h-2.5 w-0.5 bg-fg align-middle" />
              {used ? (
                <span className="font-data tabular-nums">{format(dimension.used)}</span>
              ) : (
                '—'
              )}
            </span>
            <span>
              limits{' '}
              <span className="font-data tabular-nums">
                {dimension.limited > 0 ? percentText(dimension.limited_percent) : '—'}
              </span>
            </span>
          </>
        ) : null
      }
    />
  )
}

/**
 * Placement is the fragmentation answer drawn: what is unreserved across the
 * nodes an ordinary pod could land on, and how much of it one node can take.
 * The second is what a pending pod is measured against.
 */
function Placement({ placement }: { placement: PlacementSummary }) {
  if (placement.placeable_nodes === 0 || !placement.largest_cpu || !placement.largest_memory) {
    return (
      <Notice tone="warn">
        No node takes an ordinary pod right now — each is not Ready, cordoned, tainted or full.
      </Notice>
    )
  }
  const { free, largest_cpu: cpu, largest_memory: memory } = placement
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <FitBar
        label="CPU"
        share={fitShare(cpu.cpu, free.cpu)}
        largest={formatCPU(cpu.cpu)}
        free={formatCPU(free.cpu)}
        node={cpu.node}
      />
      <FitBar
        label="Memory"
        share={fitShare(memory.memory, free.memory)}
        largest={formatMemory(memory.memory)}
        free={formatMemory(free.memory)}
        node={memory.node}
      />
      <p className="text-[11.5px] text-faint">
        <span className="font-data tabular-nums">{placement.placeable_nodes}</span>{' '}
        {placement.placeable_nodes === 1 ? 'node takes' : 'nodes take'} ordinary pods
      </p>
    </div>
  )
}

/** The one key every picture on the page shares. */
function RingLegend() {
  return (
    <span className="flex flex-wrap items-center gap-x-3 text-[11.5px] text-faint">
      <span className="inline-flex items-center gap-1.5">
        <span aria-hidden="true" className="h-2 w-4 rounded-full bg-ok" />
        reserved
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span aria-hidden="true" className="h-3 w-0.5 bg-fg" />
        in use
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span aria-hidden="true" className="size-1.5 rounded-full bg-warn" />
        90% line
      </span>
    </span>
  )
}

export function NodeCapacity() {
  const { user } = useAuth()
  const { clusters, loading: clustersLoading } = useClusters()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)
  const [methodologyOpen, setMethodologyOpen] = useDisclosureState(
    'node-capacity.methodology',
    user?.id ?? null,
  )

  const cluster =
    clusters.find(
      (entry) =>
        entry.id === clusterId && entry.connection_mode === 'agent' && entry.agent_attached,
    ) ?? null
  const unreachable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)
  // Which row the heatmap last asked for, and how many times — a counter so
  // asking for the same node twice still brings it back into view.
  const [focused, setFocused] = useState<{ name: string; count: number } | null>(null)

  const report = useCachedQuery(
    cluster ? queryKey('capacity', cluster.id) : null,
    async () => {
      if (!cluster) throw new Error('no cluster is selected')
      return fetchClusterCapacity(cluster.id)
    },
    // Allocation moves whenever anything is scheduled, and this page is opened
    // precisely while somebody is waiting for something to schedule.
    { live: true },
  )

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Capacity">
        <div className="card">
          <EmptyState
            icon={<Cpu aria-hidden="true" className="size-5" />}
            title={
              unreachable
                ? `${unreachable.name} has no live connection`
                : 'That cluster is not registered'
            }
          >
            {unreachable ? (
              <>
                Allocation is read on demand through the agent tunnel, so a cluster without one has
                nothing to show.{' '}
                <Link
                  to={`/clusters/${unreachable.id}/summary`}
                  className="text-accent hover:underline"
                >
                  Open the cluster
                </Link>{' '}
                to check its connection.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read its capacity.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const loaded = report.data
  const loading = report.loading || report.revalidating
  const nodes = loaded?.nodes ?? []
  const summary = loaded?.summary
  const attention =
    (summary?.severity_counts.danger ?? 0) + (summary?.severity_counts.warn ?? 0)
  const usageAvailable = loaded?.available ?? false

  return (
    <AppShell
      title="Capacity"
      fullWidth
      actions={<LiveRefresh query={report} />}
    >
      <div className="flex min-w-0 flex-col gap-4">
        {report.error ? (
          <Notice tone="error">
            {errorMessage(report.error, 'Could not read this cluster’s capacity.')}
          </Notice>
        ) : null}

        {loaded && !loaded.available ? <Notice tone="info">{loaded.reason}</Notice> : null}
        {loaded && loaded.available && !loaded.pod_usage_available && loaded.pod_usage_reason ? (
          <Notice tone="info">{loaded.pod_usage_reason}</Notice>
        ) : null}

        {/* Pods with nowhere to go are the other half of an oversubscription
            report, and the scheduler's own sentence says more about why than
            any arithmetic here could. */}
        {loaded && loaded.unscheduled_pods > 0 ? (
          <Notice tone="warn">
            {loaded.unscheduled_pods} {loaded.unscheduled_pods === 1 ? 'pod is' : 'pods are'} waiting
            for somewhere to run.{' '}
            {loaded.unscheduled.map((pod) => `${pod.namespace}/${pod.name}`).join(', ')}
            {loaded.unscheduled_pods > loaded.unscheduled.length ? ', and others' : ''}.
            {loaded.unscheduled[0]?.reason ? ` The scheduler says: ${loaded.unscheduled[0].reason}` : ''}
          </Notice>
        ) : null}

        {summary && summary.nodes > 0 ? (
          <>
            <div className="card grid min-w-0 gap-6 px-5 py-5 lg:grid-cols-[minmax(0,3fr)_minmax(0,1.4fr)]">
              <section aria-labelledby="capacity-reserved" className="flex min-w-0 flex-col">
                <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
                  <h2 id="capacity-reserved" className="text-[16px] font-bold text-fg">
                    Reserved
                  </h2>
                  <RingLegend />
                </div>
                <div className="mt-4 grid flex-1 grid-cols-1 items-center gap-6 sm:grid-cols-3">
                  <ClusterRing
                    label="CPU"
                    dimension={summary.cpu}
                    format={formatCPU}
                    usageAvailable={usageAvailable}
                  />
                  <ClusterRing
                    label="Memory"
                    dimension={summary.memory}
                    format={formatMemory}
                    usageAvailable={usageAvailable}
                  />
                  <CapacityRing
                    label="Pods"
                    reservedPercent={summary.pods.percent}
                    usedPercent={null}
                    measured={summary.pods.allocatable > 0}
                    amount={`${summary.pods.scheduled} / ${summary.pods.allocatable}`}
                    title={
                      `Pod slots: ${summary.pods.scheduled} of ${summary.pods.allocatable} taken` +
                      (summary.pods.without_requests > 0
                        ? `, ${summary.pods.without_requests} reserving nothing`
                        : '')
                    }
                    footer={
                      summary.pods.without_requests > 0 ? (
                        <span title="Pods that request neither CPU nor memory">
                          <span className="font-data tabular-nums">
                            {summary.pods.without_requests}
                          </span>{' '}
                          reserve nothing
                        </span>
                      ) : null
                    }
                  />
                </div>
              </section>

              <div className="flex min-w-0 flex-col gap-5 border-line-soft lg:border-l lg:pl-6">
                <section aria-labelledby="capacity-nodes" className="min-w-0">
                  <h2 id="capacity-nodes" className="mb-2 flex items-baseline gap-2 text-[13px] font-semibold text-fg">
                    <Server aria-hidden="true" className="size-3.5 self-center text-muted" />
                    Nodes
                    <span className="font-data text-[13px] text-fg tabular-nums">{summary.nodes}</span>
                    {attention > 0 ? (
                      <span className="ml-auto">
                        <Pill tone="warn">{attention} need attention</Pill>
                      </span>
                    ) : null}
                  </h2>
                  <SegmentBar
                    label="Nodes"
                    segments={nodeStateSegments(nodes)}
                  />
                </section>
                <section aria-labelledby="capacity-fit" className="min-w-0">
                  <h2
                    id="capacity-fit"
                    className="mb-2 text-[13px] font-semibold text-fg"
                    title="The most one node can take, against what is free across every node that takes ordinary pods. A pod asking for more will not schedule, however much is free in total."
                  >
                    Largest pod that fits
                  </h2>
                  <Placement placement={summary.placement} />
                </section>
                <section aria-labelledby="capacity-qos" className="min-w-0">
                  <h2 id="capacity-qos" className="mb-2 text-[13px] font-semibold text-fg">
                    QoS
                  </h2>
                  <SegmentBar label="Pods by QoS class" segments={qosSegments(summary.qos)} />
                </section>
              </div>
            </div>

            <div className="card min-w-0 px-5 pt-4 pb-5">
              <h2 className="mb-3 text-[16px] font-bold text-fg">Heatmap</h2>
              <CapacityHeatmap
                nodes={nodes}
                usageAvailable={usageAvailable}
                onSelect={(name) =>
                  setFocused((current) => ({
                    name,
                    count: current?.name === name ? current.count + 1 : 1,
                  }))
                }
              />
            </div>
          </>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-5 pt-4 pb-3.5">
            <h2 className="text-[16px] font-bold text-fg">Nodes</h2>
            <RingLegend />
            {loading ? (
              <span role="status" className="text-[12.5px] text-muted">
                Reading the cluster…
              </span>
            ) : null}
          </div>

          {loading && !loaded ? (
            <TableSkeleton columns={3} rows={5} label="Reading capacity" />
          ) : null}

          {!loading && loaded && nodes.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              This cluster reports no nodes.
            </p>
          ) : null}

          {nodes.length > 0 ? (
            <ul className="divide-y divide-line-soft">
              {nodes.map((node) => (
                <NodeRow
                  key={node.name}
                  node={node}
                  focus={focused?.name === node.name ? focused.count : 0}
                  usageAvailable={usageAvailable}
                />
              ))}
            </ul>
          ) : null}
        </div>

        <Disclosure
          open={methodologyOpen}
          onOpenChange={setMethodologyOpen}
          summary="Where these numbers come from"
        >
          <p className="text-[12px] leading-relaxed text-muted">
            Reserved and limit figures are read from the pod specs and are exact — the same
            arithmetic the scheduler does, sidecars and pod overhead included. Live usage comes
            from the cluster's Metrics API and is a single sample rather than a series; a pod
            using more than it reserved is read from the same API, per pod. QoS classes are the
            ones the cluster wrote on each pod. Where a pod could still be placed considers only
            requests, pod slots, cordons and NoSchedule/NoExecute taints — node selectors,
            affinity, topology spread and tolerations are not read. Nothing here estimates a cost
            or changes anything on the cluster.
          </p>
        </Disclosure>
      </div>
    </AppShell>
  )
}
