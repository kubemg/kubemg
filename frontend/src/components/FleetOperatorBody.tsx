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
 * Four things carry it, in the order an operator reads them.
 *
 *   1. **The slab.** The page opens on the fleet in one sentence, and on the
 *      queue when it has something in it. A landing page that reports state
 *      and asks for nothing is a page nobody opens twice, so the queue names
 *      what needs a decision — a tunnel that closed, a cluster registered but
 *      never dialled in, an access request waiting on an approval, an agent
 *      behind the rest of the fleet. On a fleet with nothing waiting the queue
 *      is *absent* rather than a list saying "all clear", and the slab says how
 *      the fleet is linked instead.
 *   2. **Four tiles of decisions, beside the fleet's capacity.** Requests
 *      waiting, calls refused, kubeconfigs about to expire, agents behind —
 *      each a link onto what it counts (see `lib/fleetStrip.ts`) — and a card
 *      of each cluster's live use against what it has.
 *   3. **A table, banded by environment.** Rows are two lines — a cluster is its
 *      name over what its link is doing — which is what buys back the columns a
 *      dense table would have spent saying the same thing twice.
 *   4. **Beside it, how the fleet's links read and what was just done.** The
 *      links are counted off the list the page already has; the activity is
 *      the newest few records of the audit trail, one read when the page opens.
 *
 * The strip's counts are read by the page, once, when it opens — two counts
 * on existing routes, never a fan-out and never on the live tick. The capacity
 * fan-out runs for an administrator only, because the developer's body draws no
 * cluster capacity at all.
 */

import { Fragment, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { AlertTriangle, ChevronRight, Plus, Timer } from 'lucide-react'
import { fetchAudit, fetchNodeMetrics } from '../api/client'
import type { AuditPage, Cluster, Environment, UsageSummary } from '../api/types'
import { LinkStatus } from './LinkStatus'
import { FleetStrip } from './FleetStrip'
import { Age, EnvironmentDot, EnvironmentTag, Table, Td, Th } from './primitives'
import {
  activityLine,
  activityRefused,
  fleetLinkCounts,
  fleetQueue,
  isBehind,
  newestAgentVersion,
} from '../lib/fleet'
import type { QueueItem } from '../lib/fleet'
import { operatorFigures } from '../lib/fleetStrip'
import type { StripCounts } from '../lib/fleetStrip'
import { clusterHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { LINK_LABEL, TONE_FILL, linkState } from '../lib/status'
import type { LinkState } from '../lib/status'
import { formatCPU, formatMemory, usageTone } from '../lib/units'

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

/* ------------------------------------------------------------------- slab --- */

/*
 * The page's masthead. With something waiting it is the queue — each item a
 * link with its action as a lime pill. With nothing waiting the queue is not
 * drawn at all, and the slab says how the fleet is linked instead: the plate
 * is always there, the list on it only when it means something.
 */
function FleetSlab({ clusters, items }: { clusters: Cluster[]; items: QueueItem[] }) {
  const links = fleetLinkCounts(clusters)
  const linked = links.live + links.direct
  const waiting = items.length > 0

  const heading = waiting
    ? items.length === 1
      ? 'One thing is waiting on you'
      : `${items.length} things are waiting on you`
    : `${linked} of ${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'} linked`

  return (
    <section aria-labelledby="fleet-slab-heading" className="slab rounded-card px-5 py-6 sm:px-7">
      <p className="text-[12.5px] font-semibold text-slab-muted">
        {waiting ? 'Needs you' : 'Fleet'}
      </p>
      <h2
        id="fleet-slab-heading"
        className="mt-1 text-[24px] font-bold text-slab-text sm:text-[28px]"
      >
        {heading}
      </h2>
      <p className="mt-1.5 text-[14px] text-slab-muted">
        {waiting
          ? `${linked} of ${clusters.length} ${clusters.length === 1 ? 'cluster is' : 'clusters are'} linked; these need an administrator before they can move.`
          : 'Every tunnel and every request is where it should be.'}
      </p>
      {waiting ? (
        <ul className="mt-4 flex flex-col gap-1">
          {items.map((item) => (
            <li key={item.key}>
              <Link
                to={item.to}
                className="group grid grid-cols-[18px_minmax(0,1fr)_auto] items-center gap-x-3.5 gap-y-1 rounded-control px-2 py-2 transition-colors duration-300 hover:bg-slab-text/10 sm:grid-cols-[18px_170px_minmax(0,1fr)_auto]"
              >
                {item.tone === 'bad' ? (
                  <AlertTriangle aria-hidden="true" className="size-4 shrink-0 text-slab-danger" />
                ) : (
                  <Timer aria-hidden="true" className="size-4 shrink-0 text-slab-warn" />
                )}
                <span className="truncate font-mono text-[13.5px] font-semibold text-slab-text">
                  {item.subject}
                </span>
                <span className="col-start-2 text-[13px] text-slab-muted sm:col-start-3">
                  {item.detail}
                </span>
                <span className="col-start-2 inline-flex items-center gap-1.5 justify-self-start rounded-full bg-accent-fill px-3 py-1 text-[12.5px] font-semibold text-on-accent sm:col-start-4 sm:justify-self-end">
                  {item.action}
                  <ChevronRight aria-hidden="true" className="size-3.5" />
                </span>
              </Link>
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  )
}

/* --------------------------------------------------------------- capacity --- */

/* One track: its name and reading above, an 8px bar under, coloured by use. */
function CapacityBar({ label, percent, title }: { label: string; percent: number; title: string }) {
  const fill = Math.min(100, Math.max(0, percent))
  return (
    <div className="flex min-w-0 flex-col gap-1.5" title={title}>
      <span className="flex items-baseline justify-between gap-2 text-[12px] text-muted">
        <span>{label}</span>
        <span className="font-mono font-semibold text-fg tabular-nums">{Math.round(percent)}%</span>
      </span>
      <span
        role="meter"
        aria-label={label}
        aria-valuenow={Math.round(percent)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={title}
        className="block h-2 overflow-hidden rounded-full bg-raised"
      >
        <span
          className={`block h-full rounded-full ${TONE_FILL[usageTone(percent)]}`}
          style={{ width: `${fill}%` }}
        />
      </span>
    </div>
  )
}

/**
 * Each cluster's live use against what it has, one row per cluster the
 * fan-out reached; a cluster it could not read says why in its row instead
 * of leaving a gap. The summed figure is the header's caption, and only once
 * two clusters contribute — a sum of one is the thing itself.
 */
function FleetCapacity({
  clusters,
  capacity,
}: {
  clusters: Cluster[]
  capacity: FleetCapacityState
}) {
  const { total } = capacity
  const contributing = Object.keys(capacity.byCluster).length
  const summed = contributing > 1 && total ? total : null
  const shown = clusters.slice(0, FLEET_METRICS_LIMIT)

  return (
    <section className="card flex min-w-0 flex-col shadow-deck">
      <header className="flex flex-wrap items-start justify-between gap-3 px-5 pt-4 pb-2">
        <div className="min-w-0">
          <h3 className="text-[16px] font-bold text-fg">Fleet capacity</h3>
          <p className="mt-0.5 text-[13px] text-muted">
            {summed
              ? `${formatCPU(summed.cpu_millicores)} of ${formatCPU(summed.cpu_capacity_millicores)} CPU, ${formatMemory(summed.memory_bytes)} of ${formatMemory(summed.memory_capacity_bytes)} memory across ${summed.nodes} nodes`
              : 'Live use against allocatable, read from each cluster’s Metrics API'}
          </p>
        </div>
        {capacity.reading ? <span className="text-[12px] text-faint">Reading…</span> : null}
      </header>
      <ul className="flex flex-col px-5 pb-3">
        {shown.map((cluster) => {
          const usage = capacity.byCluster[cluster.id]
          return (
            <li
              key={cluster.id}
              className="grid grid-cols-1 items-center gap-x-4 gap-y-2 border-b border-line-soft py-3 last:border-b-0 sm:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_minmax(0,1fr)]"
            >
              <Link
                to={clusterHref(cluster)}
                className="flex min-w-0 items-center gap-2 font-mono text-[13px] text-fg hover:text-accent"
              >
                <EnvironmentDot environment={cluster.environment} />
                <span className="truncate">{cluster.name}</span>
              </Link>
              {usage ? (
                <>
                  <CapacityBar
                    label="CPU"
                    percent={usage.cpu_percent}
                    title={`${formatCPU(usage.cpu_millicores)} / ${formatCPU(usage.cpu_capacity_millicores)}`}
                  />
                  <CapacityBar
                    label="Memory"
                    percent={usage.memory_percent}
                    title={`${formatMemory(usage.memory_bytes)} / ${formatMemory(usage.memory_capacity_bytes)}`}
                  />
                </>
              ) : (
                <span className="sm:col-span-2">
                  <CapacityGap cluster={cluster} />
                </span>
              )}
            </li>
          )
        })}
      </ul>
      {capacity.skipped > 0 || clusters.length > shown.length ? (
        <p className="border-t border-line-soft px-5 py-2.5 text-[12px] text-faint">
          {Math.max(capacity.skipped, clusters.length - shown.length)} more not read — open a
          cluster for its own numbers
        </p>
      ) : null}
    </section>
  )
}

/**
 * Why a cluster has no numbers. An unreachable one says so in its own words,
 * and one that simply has no metrics-server is not a fault at all.
 */
function CapacityGap({ cluster }: { cluster: Cluster }) {
  const failing = cluster.status === 'unhealthy'
  return (
    <span className={`text-[12.5px] ${failing ? 'text-danger' : 'text-faint'}`}>
      {failing
        ? (cluster.status_message ?? 'unreachable')
        : cluster.agent_attached
          ? 'no metrics-server'
          : 'no tunnel to read through'}
    </span>
  )
}

/* -------------------------------------------------------------- side column --- */

const LINK_ORDER: LinkState[] = ['live', 'direct', 'idle', 'down']

const LINK_FILL: Record<LinkState, string> = {
  live: 'bg-ok',
  direct: 'bg-muted',
  idle: 'bg-faint',
  down: 'bg-danger',
}

/* How the fleet's links read, as one bar and its legend. */
function FleetLinks({ clusters }: { clusters: Cluster[] }) {
  const counts = fleetLinkCounts(clusters)
  const summary = LINK_ORDER.filter((state) => counts[state] > 0)
    .map((state) => `${counts[state]} ${LINK_LABEL[state].toLowerCase()}`)
    .join(', ')

  return (
    <section className="card shadow-deck">
      <header className="px-5 pt-4">
        <h3 className="text-[16px] font-bold text-fg">Links across the fleet</h3>
        <p className="mt-0.5 text-[13px] text-muted">How each cluster reaches kubemg right now</p>
      </header>
      <div role="img" aria-label={summary} className="mx-5 mt-4 mb-3 flex h-3 gap-[3px]">
        {LINK_ORDER.filter((state) => counts[state] > 0).map((state) => (
          <span
            key={state}
            className={`block h-full rounded-[3px] ${LINK_FILL[state]}`}
            style={{ flexGrow: counts[state] }}
          />
        ))}
      </div>
      <ul className="grid grid-cols-2 gap-x-4 gap-y-2 px-5 pb-4 text-[13px]">
        {LINK_ORDER.map((state) => (
          <li key={state} className="flex items-center gap-2 text-muted">
            <span aria-hidden="true" className={`size-2.5 rounded-full ${LINK_FILL[state]}`} />
            {LINK_LABEL[state]}
            <span className="ml-auto font-mono font-semibold text-fg tabular-nums">
              {counts[state]}
            </span>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** How many records the feed shows. A glance, not a trail. */
const ACTIVITY_SHOWN = 6

/*
 * The newest few records of the audit trail, read once when the page opens —
 * deliberately not on the live tick, the same rule as the strip's counts. A
 * read that fails says so in a line; it never costs the page anything else.
 */
function RecentActivity() {
  const query = useCachedQuery<AuditPage>(queryKey('fleet-activity', ACTIVITY_SHOWN), () =>
    fetchAudit({ limit: ACTIVITY_SHOWN }),
  )
  const events = query.data?.events ?? []

  return (
    <section className="card shadow-deck">
      <header className="flex items-start justify-between gap-3 px-5 pt-4 pb-1">
        <div>
          <h3 className="text-[16px] font-bold text-fg">Recent activity</h3>
          <p className="mt-0.5 text-[13px] text-muted">The audit trail, newest first</p>
        </div>
        <Link to="/admin/audit" className="text-[13px] font-medium text-accent hover:underline">
          View all
        </Link>
      </header>
      {query.error ? (
        <p className="px-5 py-4 text-[12.5px] text-faint">The trail could not be read just now.</p>
      ) : query.loading && events.length === 0 ? (
        <p className="px-5 py-4 text-[12.5px] text-faint">Reading the trail…</p>
      ) : events.length === 0 ? (
        <p className="px-5 py-4 text-[12.5px] text-faint">Nothing has been recorded yet.</p>
      ) : (
        <ul className="flex flex-col py-2">
          {events.map((event) => (
            <li
              key={event.id}
              className="grid grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-x-3 px-5 py-2"
            >
              <span
                aria-hidden="true"
                className="row-span-2 grid size-8 place-items-center rounded-full bg-raised font-mono text-[11px] font-semibold text-fg"
              >
                {event.username.slice(0, 2).toUpperCase()}
              </span>
              <span className="flex min-w-0 items-center gap-2">
                <span className="truncate font-mono text-[13px] font-medium text-fg">
                  {event.username}
                </span>
                {activityRefused(event) ? (
                  <span className="shrink-0 rounded-chip bg-danger-soft px-1.5 text-[11px] font-medium text-danger">
                    refused
                  </span>
                ) : null}
              </span>
              <span className="text-[12px] text-faint">
                <Age iso={event.at} />
              </span>
              <span
                className="col-span-2 col-start-2 truncate font-mono text-[12px] text-muted"
                title={activityLine(event)}
              >
                {activityLine(event)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/* ------------------------------------------------------------------ table --- */

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
      <Td
        className={`hidden py-3.5 font-mono text-[12px] md:table-cell ${
          drifted ? 'text-warn' : 'text-muted'
        }`}
        title={drifted ? `behind ${newestAgent} running elsewhere in the fleet` : undefined}
      >
        {cluster.agent_version ?? '—'}
      </Td>
      <Td className="py-3.5 font-mono text-[12px] text-muted">
        <Age iso={cluster.status === 'pending' ? undefined : cluster.last_checked_at} />
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
    <section className="min-w-0 overflow-hidden rounded-card border border-line bg-surface shadow-deck">
      <header className="flex flex-wrap items-start justify-between gap-3 px-5 pt-4 pb-3">
        <div>
          <h3 className="text-[16px] font-bold text-fg">Clusters</h3>
          <p className="mt-0.5 text-[13px] text-muted">
            {clusters.length} registered, banded by environment
          </p>
        </div>
        <Link
          to="/admin/clusters/new"
          className="inline-flex h-9 items-center gap-2 rounded-control border border-line bg-surface px-3.5 text-[13.5px] font-medium text-fg transition-colors duration-300 hover:border-faint/60 hover:bg-raised"
        >
          <Plus aria-hidden="true" className="size-4" />
          Register
        </Link>
      </header>
      <Table>
        <thead>
          <tr>
            <Th className="w-[40%]">Cluster</Th>
            <Th className="w-[22%]">Kubernetes</Th>
            <Th className="hidden w-[18%] md:table-cell">Agent</Th>
            <Th className="w-[20%]">Checked</Th>
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
                  <td colSpan={4} className="px-4 py-1.5">
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
  counts,
}: {
  clusters: Cluster[]
  /** The strip's counts, read once by the page. See lib/fleetStrip.ts. */
  counts: StripCounts
}) {
  const capacity = useFleetCapacity(clusters)
  // The queue names what it can; a count it could not read adds no row.
  const pending = counts.pending ?? 0
  const queue = useMemo(() => fleetQueue(clusters, pending), [clusters, pending])
  const newestAgent = useMemo(() => newestAgentVersion(clusters), [clusters])

  const figures = useMemo(() => operatorFigures(counts, clusters), [counts, clusters])

  return (
    <>
      <FleetSlab clusters={clusters} items={queue} />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.25fr)]">
        <FleetStrip figures={figures} className="grid content-start gap-4 sm:grid-cols-2" />
        <FleetCapacity clusters={clusters} capacity={capacity} />
      </div>
      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)]">
        <FleetTable clusters={clusters} capacity={capacity} newestAgent={newestAgent} />
        <div className="flex min-w-0 flex-col gap-4">
          <FleetLinks clusters={clusters} />
          <RecentActivity />
        </div>
      </div>
    </>
  )
}
