/*
 * The fleet page an administrator arrives at.
 *
 * The developer's body beside this one is a launcher: which clusters can I
 * open, with what role, in which namespaces. Every column here is the other
 * thing — the fleet *as an installation*. Node counts, agent versions, when a
 * cluster was last probed and how much of it is in use are facts somebody has
 * to act on, and the person who can act on them is the one who registered the
 * cluster.
 *
 * Three things carry it, in the order an operator reads them.
 *
 *   1. **The queue, and only when it has something in it.** A landing page that
 *      reports state and asks for nothing is a page nobody opens twice. This
 *      one opens on what needs a decision — a tunnel that closed, a cluster
 *      registered but never dialled in, an access request waiting on an
 *      approval, an agent behind the rest of the fleet. On a fleet with nothing
 *      waiting the block is *absent* rather than an empty card saying "all
 *      clear", which is what makes its presence mean something.
 *   2. **One figures strip.** Six readings and both capacity tracks on one
 *      surface, in roughly the height three stat cards took for three.
 *   3. **A table, banded by environment.** Rows are two lines — a cluster is its
 *      name over what its link is doing — which is what buys back the columns a
 *      dense table would have spent saying the same thing twice.
 *
 * Nothing here reads anything the page did not already read, with one
 * *reduction*: the capacity fan-out now runs for an administrator only, because
 * the developer's body draws no cluster capacity at all.
 */

import { Fragment, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { AlertTriangle, ChevronRight, Timer } from 'lucide-react'
import { fetchNodeMetrics } from '../api/client'
import type { Cluster, Environment, UsageSummary } from '../api/types'
import { LinkStatus } from './LinkStatus'
import { EnvironmentTag, MiniMeter, Table, Td, Th } from './primitives'
import { fleetQueue, isBehind, newestAgentVersion } from '../lib/fleet'
import type { QueueItem } from '../lib/fleet'
import { clusterHref } from '../lib/navigation'
import { linkState } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory, ratio } from '../lib/units'

/* Bands run prod first: the fleet is read top-down by how much a cluster matters. */
const BANDS: Array<{ environment: Environment; title: string }> = [
  { environment: 'prod', title: 'Production' },
  { environment: 'staging', title: 'Staging' },
  { environment: 'dev', title: 'Development' },
]

/*
 * Fleet capacity is a fan-out: every cluster's usage is a separate read down a
 * separate tunnel, so this is the one place in the app where the cost scales
 * with fleet size. It is capped rather than paginated — past a dozen clusters
 * the honest answer is that this page is not the right place to ask, and the
 * cluster pages are — and only attached agent clusters are asked at all, since
 * anything else has nothing to answer with.
 */
export const FLEET_METRICS_LIMIT = 12

type FleetCapacityState = {
  byCluster: Record<number, UsageSummary>
  total: UsageSummary | null
  /** Clusters that were skipped because the fan-out is capped. */
  skipped: number
  reading: boolean
}

function useFleetCapacity(clusters: Cluster[]): FleetCapacityState {
  const [byCluster, setByCluster] = useState<Record<number, UsageSummary>>({})
  const [reading, setReading] = useState(false)

  const attached = useMemo(
    () => clusters.filter((cluster) => cluster.agent_attached).map((cluster) => cluster.id),
    [clusters],
  )
  const targets = useMemo(() => attached.slice(0, FLEET_METRICS_LIMIT), [attached])
  // A joined key so re-running depends on *which* clusters are attached rather
  // than on the array identity, which changes on every fleet reload.
  const key = targets.join(',')

  useEffect(() => {
    if (key === '') {
      setByCluster({})
      return
    }

    let live = true
    setReading(true)

    // Settled rather than all: one cluster without metrics-server, or one that
    // dropped its tunnel mid-read, must not blank the whole row.
    void Promise.allSettled(targets.map((id) => fetchNodeMetrics(id))).then((results) => {
      if (!live) return

      const next: Record<number, UsageSummary> = {}
      results.forEach((result, index) => {
        if (result.status !== 'fulfilled' || !result.value.available) return
        next[targets[index]] = result.value.summary
      })
      setByCluster(next)
      setReading(false)
    })

    return () => {
      live = false
    }
    // targets is derived from key; depending on both would re-run on identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  const total = useMemo(() => {
    const summaries = Object.values(byCluster)
    if (summaries.length === 0) return null

    return summaries.reduce<UsageSummary>(
      (sum, entry) => ({
        nodes: sum.nodes + entry.nodes,
        cpu_millicores: sum.cpu_millicores + entry.cpu_millicores,
        cpu_capacity_millicores: sum.cpu_capacity_millicores + entry.cpu_capacity_millicores,
        cpu_percent: 0,
        memory_bytes: sum.memory_bytes + entry.memory_bytes,
        memory_capacity_bytes: sum.memory_capacity_bytes + entry.memory_capacity_bytes,
        memory_percent: 0,
      }),
      {
        nodes: 0,
        cpu_millicores: 0,
        cpu_capacity_millicores: 0,
        cpu_percent: 0,
        memory_bytes: 0,
        memory_capacity_bytes: 0,
        memory_percent: 0,
      },
    )
  }, [byCluster])

  return { byCluster, total, skipped: attached.length - targets.length, reading }
}

/* ------------------------------------------------------------------ queue --- */

function FleetQueue({ items }: { items: QueueItem[] }) {
  // Absent rather than empty: a block that is always there stops being read.
  if (items.length === 0) return null

  return (
    <section className="overflow-hidden rounded-card border border-line bg-surface">
      <div className="flex items-center gap-2.5 border-b border-line-soft bg-raised px-3.5 py-2.5">
        <h2 className="label text-fg">Needs you</h2>
        <p className="ml-auto text-[11px] text-faint">{items.length} waiting</p>
      </div>
      <ul>
        {items.map((item) => (
          <li key={item.key}>
            <Link
              to={item.to}
              className="group grid grid-cols-[18px_minmax(0,1fr)_auto] items-center gap-x-3.5 gap-y-1 border-b border-line-soft px-3.5 py-2.5 last:border-b-0 hover:bg-raised sm:grid-cols-[18px_150px_minmax(0,1fr)_auto]"
            >
              {item.tone === 'bad' ? (
                <AlertTriangle aria-hidden="true" className="size-4 shrink-0 text-danger" />
              ) : (
                <Timer aria-hidden="true" className="size-4 shrink-0 text-warn" />
              )}
              <span className="truncate font-mono text-[13px] font-semibold text-fg">
                {item.subject}
              </span>
              <span className="col-start-2 text-[12.5px] text-muted sm:col-start-3">
                {item.detail}
              </span>
              <span className="col-start-2 inline-flex items-center gap-1 justify-self-start text-[12px] font-semibold text-accent sm:col-start-4 sm:justify-self-end">
                {item.action}
                <ChevronRight aria-hidden="true" className="size-3.5" />
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  )
}

/* ---------------------------------------------------------------- figures --- */

function Figure({ value, label, tone }: { value: string; label: string; tone?: 'bad' | 'dim' }) {
  return (
    <div className="min-w-[104px] border-r border-line-soft px-5 py-3 last:border-r-0">
      <p
        className={`font-mono text-[21px] leading-tight ${
          tone === 'bad'
            ? 'font-bold text-danger'
            : tone === 'dim'
              ? 'text-muted'
              : 'font-bold text-fg'
        }`}
      >
        {value}
      </p>
      <p className="label mt-0.5">{label}</p>
    </div>
  )
}

function FleetFigures({
  clusters,
  capacity,
}: {
  clusters: Cluster[]
  capacity: FleetCapacityState
}) {
  const reachable = clusters.filter((cluster) => cluster.status === 'healthy').length
  const unreachable = clusters.filter((cluster) => cluster.status === 'unhealthy').length
  const tunnels = clusters.filter((cluster) => cluster.agent_attached).length
  const environments = BANDS.filter(({ environment }) =>
    clusters.some((cluster) => cluster.environment === environment),
  ).length

  const { total } = capacity
  // A sum of one is the thing itself: below two contributing clusters the
  // capacity slot says nothing the cluster's own row does not already say.
  const contributing = Object.keys(capacity.byCluster).length
  const summed = contributing > 1 && total ? total : null

  return (
    <section className="flex flex-wrap items-stretch overflow-hidden rounded-card border border-line bg-surface">
      <Figure value={String(clusters.length)} label="clusters" />
      <Figure value={String(reachable)} label="reachable" />
      {unreachable > 0 ? (
        <Figure value={String(unreachable)} label="unreachable" tone="bad" />
      ) : null}
      <Figure value={String(tunnels)} label="tunnels open" />
      {total ? <Figure value={String(total.nodes)} label="nodes" /> : null}
      <Figure value={String(environments)} label="environments" tone="dim" />

      {summed ? (
        <div className="flex min-w-[220px] flex-1 flex-col justify-center gap-1.5 border-l border-line-soft px-5 py-3">
          <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3">
            <MiniMeter
              label="CPU"
              percent={ratio(summed.cpu_millicores, summed.cpu_capacity_millicores)}
            />
            <span className="font-mono text-[11.5px] whitespace-nowrap text-muted tabular-nums">
              <span className="text-fg">{formatCPU(summed.cpu_millicores)}</span> /{' '}
              {formatCPU(summed.cpu_capacity_millicores)}
            </span>
          </div>
          <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3">
            <MiniMeter
              label="MEM"
              percent={ratio(summed.memory_bytes, summed.memory_capacity_bytes)}
            />
            <span className="font-mono text-[11.5px] whitespace-nowrap text-muted tabular-nums">
              <span className="text-fg">{formatMemory(summed.memory_bytes)}</span> /{' '}
              {formatMemory(summed.memory_capacity_bytes)}
            </span>
          </div>
          {capacity.skipped > 0 ? (
            <p className="text-[11px] text-faint">
              {capacity.skipped} more not read — open a cluster for its own numbers
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  )
}

/* ------------------------------------------------------------------ table --- */

/**
 * The resources cell. A cluster with no numbers gets the reason instead of a
 * gap — an unreachable one says so in its own words, and one that simply has no
 * metrics-server is not a fault at all.
 */
function ResourcesCell({ cluster, usage }: { cluster: Cluster; usage?: UsageSummary }) {
  if (!usage) {
    const failing = cluster.status === 'unhealthy'
    return (
      <span className={`text-[11.5px] ${failing ? 'text-danger' : 'text-faint'}`}>
        {failing
          ? (cluster.status_message ?? 'unreachable')
          : cluster.agent_attached
            ? 'no metrics-server'
            : 'no tunnel to read through'}
      </span>
    )
  }

  return (
    <span className="flex min-w-[132px] flex-col gap-1">
      <MiniMeter
        label="CPU"
        percent={usage.cpu_percent}
        title={`${formatCPU(usage.cpu_millicores)} / ${formatCPU(usage.cpu_capacity_millicores)}`}
      />
      <MiniMeter
        label="MEM"
        percent={usage.memory_percent}
        title={`${formatMemory(usage.memory_bytes)} / ${formatMemory(usage.memory_capacity_bytes)}`}
      />
    </span>
  )
}

function ClusterRow({
  cluster,
  usage,
  newestAgent,
}: {
  cluster: Cluster
  usage?: UsageSummary
  newestAgent: string | null
}) {
  const failing = cluster.status === 'unhealthy'
  const drifted = Boolean(
    cluster.agent_version && newestAgent && isBehind(cluster.agent_version, newestAgent),
  )

  return (
    <tr className="group border-t border-line-soft transition-colors hover:bg-raised/70">
      <Td className="py-3.5">
        <Link to={clusterHref(cluster)} className="flex min-w-0 items-center gap-2.5">
          <LinkStatus state={linkState(cluster)} variant="glyph" className="shrink-0" />
          <span className="flex min-w-0 flex-col gap-0.5">
            <span
              className={`truncate font-mono text-[13.5px] leading-tight font-semibold ${
                failing ? 'text-danger' : 'text-fg'
              } group-hover:text-accent`}
            >
              {cluster.name}
            </span>
            <span
              className={`truncate text-[11px] leading-tight ${failing ? 'text-danger' : 'text-faint'}`}
            >
              {cluster.connection_mode === 'agent' ? 'agent · outbound' : 'api server · direct'}
            </span>
          </span>
        </Link>
      </Td>
      <Td className="py-3.5">
        <span className="flex flex-col gap-0.5">
          <span className="font-mono text-[12.5px] leading-tight text-fg">
            {cluster.kubernetes_version ?? '—'}
          </span>
          <span className="text-[11px] leading-tight text-faint">
            {usage ? `${usage.nodes} ${usage.nodes === 1 ? 'node' : 'nodes'}` : 'nodes not read'}
          </span>
        </span>
      </Td>
      <Td className="py-3.5">
        <ResourcesCell cluster={cluster} usage={usage} />
      </Td>
      <Td
        className={`hidden py-3.5 font-mono text-[12px] md:table-cell ${
          drifted ? 'text-warn' : 'text-muted'
        }`}
        title={drifted ? `behind ${newestAgent} running elsewhere in the fleet` : undefined}
      >
        {cluster.agent_version ?? '—'}
      </Td>
      <Td className="py-3.5 font-mono text-[12px] text-muted">
        {cluster.status === 'pending' ? 'never' : relativeAge(cluster.last_checked_at)}
      </Td>
    </tr>
  )
}

function FleetTable({
  clusters,
  capacity,
  newestAgent,
}: {
  clusters: Cluster[]
  capacity: FleetCapacityState
  newestAgent: string | null
}) {
  return (
    <section className="overflow-hidden rounded-card border border-line bg-surface">
      <Table>
        <thead>
          <tr>
            <Th className="w-[30%]">Cluster</Th>
            <Th className="w-[16%]">Kubernetes</Th>
            <Th className="w-[24%]">Resources</Th>
            <Th className="hidden w-[15%] md:table-cell">Agent</Th>
            <Th className="w-[15%]">Checked</Th>
          </tr>
        </thead>
        <tbody>
          {BANDS.map(({ environment, title }) => {
            const band = clusters.filter((cluster) => cluster.environment === environment)
            if (band.length === 0) return null
            const failing = band.filter((cluster) => cluster.status === 'unhealthy').length

            return (
              <Fragment key={environment}>
                <tr className="border-t border-line-soft bg-sunken">
                  <td colSpan={5} className="px-4 py-1.5">
                    <div className="flex items-center gap-2.5">
                      <span className="label text-fg">{title}</span>
                      <EnvironmentTag environment={environment} />
                      <span className="ml-auto text-[11px] text-faint">
                        {band.length} {band.length === 1 ? 'cluster' : 'clusters'}
                        {failing > 0 ? (
                          <span className="text-danger"> · {failing} failing</span>
                        ) : null}
                      </span>
                    </div>
                  </td>
                </tr>
                {band.map((cluster) => (
                  <ClusterRow
                    key={cluster.id}
                    cluster={cluster}
                    usage={capacity.byCluster[cluster.id]}
                    newestAgent={newestAgent}
                  />
                ))}
              </Fragment>
            )
          })}
        </tbody>
      </Table>
    </section>
  )
}

/* ------------------------------------------------------------------- body --- */

export function FleetOperatorBody({
  clusters,
  pendingRequests,
}: {
  clusters: Cluster[]
  /** Access requests waiting on a decision, or 0 when none could be read. */
  pendingRequests: number
}) {
  const capacity = useFleetCapacity(clusters)
  const queue = useMemo(() => fleetQueue(clusters, pendingRequests), [clusters, pendingRequests])
  const newestAgent = useMemo(() => newestAgentVersion(clusters), [clusters])

  return (
    <>
      <FleetQueue items={queue} />
      <FleetFigures clusters={clusters} capacity={capacity} />
      <FleetTable clusters={clusters} capacity={capacity} newestAgent={newestAgent} />
    </>
  )
}
