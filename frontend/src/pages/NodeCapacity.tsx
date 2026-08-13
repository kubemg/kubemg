import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { ChevronDown, Cpu, RefreshCw } from 'lucide-react'
import { errorMessage, fetchClusterCapacity } from '../api/client'
import type {
  CapacityDimension,
  CapacitySeverity,
  NodeCapacity as NodeCapacityRow,
  PodSlots,
} from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, EmptyState, Notice, Pill } from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import { queryKey, useCachedQuery } from '../lib/query'
import { formatCPU, formatMemory } from '../lib/units'
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

/* The tone ladder for a percentage against allocatable. It mirrors the
   thresholds the server reasons with, and never contradicts them: this only
   colours a bar, the concern list beneath it is what makes the claim. */
function allocationTone(percent: number): 'ok' | 'warn' | 'bad' {
  if (percent >= 100) return 'bad'
  if (percent >= 90) return 'warn'
  return 'ok'
}

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

const FILL_TONE = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-danger' } as const

/**
 * AllocationBar is one resource on one node: a track filled to what is
 * reserved, a tick where consumption actually sits, and the limit written
 * underneath because it cannot be drawn to the same scale.
 */
function AllocationBar({
  label,
  dimension,
  format,
}: {
  label: string
  dimension: CapacityDimension
  format: (value: number) => string
}) {
  const reserved = Math.min(100, Math.max(0, dimension.requested_percent))
  const used = Math.min(100, Math.max(0, dimension.used_percent))
  const tone = allocationTone(dimension.requested_percent)
  const measured = dimension.allocatable > 0

  return (
    <div className="min-w-0">
      <div className="flex items-baseline gap-2">
        <span className="label">{label}</span>
        <span className="ml-auto font-mono text-[12.5px] text-fg tabular-nums">
          {format(dimension.requested)}
        </span>
        <span className="font-mono text-[12px] text-faint tabular-nums">
          / {measured ? format(dimension.allocatable) : 'unknown'}
        </span>
      </div>

      <div
        role="meter"
        aria-label={`${label} reserved`}
        aria-valuenow={measured ? Math.round(dimension.requested_percent) : undefined}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={
          measured
            ? `${format(dimension.requested)} of ${format(dimension.allocatable)} reserved, ` +
              `${format(dimension.used)} in use`
            : 'no allocatable capacity reported'
        }
        className="relative mt-1.5 h-2 overflow-hidden rounded-full bg-raised"
      >
        <span
          aria-hidden="true"
          className={`block h-full rounded-full ${FILL_TONE[tone]}`}
          style={{ width: `${reserved}%` }}
        />
        {/* Consumption, on the same track and the same scale. It is a tick and
            not a second fill because two overlapping fills read as one bar of
            an ambiguous length, and the distance between the two marks is the
            whole point of the page. */}
        {dimension.used > 0 ? (
          <span
            aria-hidden="true"
            className="absolute inset-y-0 w-0.5 bg-fg"
            style={{ left: `calc(${used}% - 1px)` }}
          />
        ) : null}
      </div>

      <p className="mt-1 flex flex-wrap gap-x-3 font-mono text-[11px] text-faint tabular-nums">
        <span>{dimension.requested_percent.toFixed(0)}% reserved</span>
        <span>
          {dimension.used > 0 ? `${format(dimension.used)} used` : 'no live usage'}
        </span>
        <span>
          {dimension.limited > 0
            ? `limits ${dimension.limited_percent.toFixed(0)}% of the node`
            : 'no limits declared'}
        </span>
      </p>
    </div>
  )
}

/** PodSlotBar is the third ceiling, and the one that binds before the others do. */
function PodSlotBar({ slots }: { slots: PodSlots }) {
  const tone = allocationTone(slots.percent)
  return (
    <div className="min-w-0">
      <div className="flex items-baseline gap-2">
        <span className="label">Pod slots</span>
        <span className="ml-auto font-mono text-[12.5px] text-fg tabular-nums">
          {slots.scheduled}
        </span>
        <span className="font-mono text-[12px] text-faint tabular-nums">
          / {slots.allocatable > 0 ? slots.allocatable : 'unknown'}
        </span>
      </div>
      <div
        role="meter"
        aria-label="Pod slots"
        aria-valuenow={Math.round(slots.percent)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={`${slots.scheduled} of ${slots.allocatable} pod slots taken`}
        className="mt-1.5 h-2 overflow-hidden rounded-full bg-raised"
      >
        <span
          aria-hidden="true"
          className={`block h-full rounded-full ${FILL_TONE[tone]}`}
          style={{ width: `${Math.min(100, Math.max(0, slots.percent))}%` }}
        />
      </div>
      <p className="mt-1 font-mono text-[11px] text-faint tabular-nums">
        {slots.percent.toFixed(0)}% taken
        {slots.without_requests > 0 ? ` · ${slots.without_requests} reserving nothing` : ''}
      </p>
    </div>
  )
}

/** NodeRow is one node: its three ceilings, and what its numbers say. */
function NodeRow({ node }: { node: NodeCapacityRow }) {
  const [open, setOpen] = useState(false)
  const worst = node.concerns[0]

  return (
    <li className="min-w-0 px-4 py-4">
      <div className="flex flex-wrap items-center gap-2.5">
        <span className="min-w-0 truncate font-mono text-[13px] text-fg">{node.name}</span>
        {node.roles.map((role) => (
          <span key={role} className="label text-faint">
            {role}
          </span>
        ))}
        {!node.ready ? <Pill tone="bad">not ready</Pill> : null}
        {!node.schedulable ? <Pill tone="warn">cordoned</Pill> : null}
        <span className="ml-auto">
          <Pill tone={SEVERITY_TONE[node.severity]}>{SEVERITY_LABEL[node.severity]}</Pill>
        </span>
      </div>

      <div className="mt-3 grid gap-4 sm:grid-cols-3">
        <AllocationBar label="CPU" dimension={node.cpu} format={formatCPU} />
        <AllocationBar label="Memory" dimension={node.memory} format={formatMemory} />
        <PodSlotBar slots={node.pods} />
      </div>

      {node.concerns.length > 0 ? (
        <div className="mt-3">
          {/* The worst line is always visible; the rest is one click away. A
              node with six notes must not push the next node off the screen. */}
          <p className="text-[12.5px] leading-relaxed text-muted">
            <span className="font-medium text-fg">{worst.title}.</span> {worst.detail}
          </p>
          {node.concerns.length > 1 || node.top_requests.length > 0 ? (
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen((current) => !current)}
              className="mt-2 inline-flex items-center gap-1.5 text-[12px] text-accent hover:underline"
            >
              <ChevronDown
                aria-hidden="true"
                className={`size-3.5 transition-transform ${open ? 'rotate-180' : ''}`}
              />
              {open ? 'Less' : `${node.concerns.length - 1} more, and what is holding this node`}
            </button>
          ) : null}
        </div>
      ) : null}

      {open ? (
        <div className="mt-3 flex flex-col gap-3 rounded-control border border-line-soft bg-sunken p-3">
          {node.concerns.slice(1).map((concern) => (
            <p key={concern.code} className="text-[12.5px] leading-relaxed text-muted">
              <Pill tone={SEVERITY_TONE[concern.severity]}>{concern.title}</Pill>{' '}
              <span className="ml-1">{concern.detail}</span>
            </p>
          ))}

          {node.top_requests.length > 0 ? (
            <div>
              <p className="label mb-1.5 text-faint">Largest reservations on this node</p>
              <ul className="flex flex-col gap-1">
                {node.top_requests.map((pod) => (
                  <li
                    key={`${pod.namespace}/${pod.name}`}
                    className="flex flex-wrap items-baseline gap-x-2.5 font-mono text-[12px]"
                  >
                    <span className="text-faint">{pod.namespace}</span>
                    <span className="min-w-0 truncate text-fg">{pod.name}</span>
                    {/* Only the resources this pod actually reserves. A pod
                        asking for CPU and nothing else would otherwise read as
                        "250m · 0", which looks like a memory request of zero
                        rather than the absence of one. */}
                    <span className="ml-auto text-muted tabular-nums">
                      {[
                        pod.cpu_millicores > 0 ? formatCPU(pod.cpu_millicores) : null,
                        pod.memory_bytes > 0 ? formatMemory(pod.memory_bytes) : null,
                      ]
                        .filter(Boolean)
                        .join(' · ')}
                    </span>
                    <span className="w-12 text-right text-faint tabular-nums">
                      {pod.share_percent.toFixed(0)}%
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  )
}

export function NodeCapacity() {
  const { clusters, loading: clustersLoading } = useClusters()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)

  const cluster =
    clusters.find(
      (entry) =>
        entry.id === clusterId && entry.connection_mode === 'agent' && entry.agent_attached,
    ) ?? null
  const unreachable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  const report = useCachedQuery(
    cluster ? queryKey('capacity', cluster.id) : null,
    async () => {
      if (!cluster) throw new Error('no cluster is selected')
      return fetchClusterCapacity(cluster.id)
    },
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

  return (
    <AppShell
      title="Capacity"
      fullWidth
      actions={
        <Button onClick={() => void report.refresh()} disabled={loading}>
          <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {report.error ? (
          <Notice tone="error">
            {errorMessage(report.error, 'Could not read this cluster’s capacity.')}
          </Notice>
        ) : null}

        {loaded && !loaded.available ? <Notice tone="info">{loaded.reason}</Notice> : null}

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
          <div className="card min-w-0 p-4">
            <div className="flex flex-wrap items-center gap-2.5">
              <h2 className="text-[14px] font-semibold text-fg">
                {summary.nodes} {summary.nodes === 1 ? 'node' : 'nodes'}
              </h2>
              {attention > 0 ? (
                <Pill tone="warn">{attention} needing attention</Pill>
              ) : (
                <Pill tone="ok">nothing pressing</Pill>
              )}
              {summary.schedulable < summary.nodes ? (
                <Pill tone="idle">{summary.nodes - summary.schedulable} cordoned</Pill>
              ) : null}
              {summary.ready < summary.nodes ? (
                <Pill tone="bad">{summary.nodes - summary.ready} not ready</Pill>
              ) : null}
            </div>
            <div className="mt-3 grid gap-4 sm:grid-cols-3">
              <AllocationBar label="CPU reserved" dimension={summary.cpu} format={formatCPU} />
              <AllocationBar
                label="Memory reserved"
                dimension={summary.memory}
                format={formatMemory}
              />
              <PodSlotBar slots={summary.pods} />
            </div>
          </div>
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">Nodes</h2>
            {loading ? <span className="text-[12px] text-muted">Reading the cluster…</span> : null}
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
                <NodeRow key={node.name} node={node} />
              ))}
            </ul>
          ) : null}
        </div>

        <p className="text-[12px] leading-relaxed text-muted">
          Reserved and limit figures are read from the pod specs and are exact — the same arithmetic
          the scheduler does, sidecars and pod overhead included. Live usage comes from the cluster's
          Metrics API and is a single sample rather than a series. Nothing here changes anything on
          the cluster.{' '}
          <Link to={`/clusters/${clusterId}/cost`} className="text-accent hover:underline">
            Cost
          </Link>{' '}
          puts your own rates on these same reservations.
        </p>
      </div>
    </AppShell>
  )
}
