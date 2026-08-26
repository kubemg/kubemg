/*
 * The fleet page a developer arrives at.
 *
 * The administrator's body beside this one is about the fleet *as an
 * installation* — node counts, agent versions, when each cluster was last
 * probed, how much of it is in use. `ClusterWorkloadSummary` already makes the
 * argument for why none of that belongs here, one level down: "a developer
 * cannot act on any of them. Handed to them it is not detail, it is noise in
 * front of the two questions they came with." On the fleet page the questions
 * are one step earlier — which of these can I open, and with what — so this is
 * a launcher rather than a dashboard.
 *
 * Four things, in the order they are wanted:
 *
 *   1. **What you can reach**, in a sentence. Not "1/1 reachable", which is an
 *      operator's framing of the same number.
 *   2. **A live elevation**, when there is one. It is the only thing on this
 *      page with a deadline, so it is the only thing that gets a coloured
 *      strip and a countdown.
 *   3. **Where you were last** — the Grafana lesson, that a personal landing
 *      page is recency rather than inventory. It costs nothing: the same
 *      browser-local store the deck switch and `lib/favorites.ts` already use.
 *   4. **Rows carrying your own access.** Your role and your namespaces are the
 *      two facts that decide what happens after the click.
 *
 * Capacity is deliberately absent. `/metrics/nodes` is a cluster-wide read and
 * is refused to a namespace-scoped grant, so drawing it here would be a column
 * that reads empty for exactly the people this body exists for — and the fleet
 * fan-out it would cost is the one read in the app whose price scales with the
 * size of the fleet.
 *
 * Nothing here is a read the page did not already make, except the requests
 * list the caller can read about themselves anyway.
 */

import { useMemo } from 'react'
import { Link } from 'react-router'
import { ChevronRight, KeyRound } from 'lucide-react'
import type { Cluster, Environment, JitRequest } from '../api/types'
import { LinkStatus } from './LinkStatus'
import { Button, EnvironmentTag } from './primitives'
import { clusterHref, hasTunnel } from '../lib/navigation'
import { useRecentClusters } from '../lib/recents'
import { linkState } from '../lib/status'
import { formatCountdown, useCountdown } from '../lib/time'

/* Bands run prod first, as everywhere else the fleet is listed. */
const BANDS: Array<{ environment: Environment; title: string }> = [
  { environment: 'prod', title: 'Production' },
  { environment: 'staging', title: 'Staging' },
  { environment: 'dev', title: 'Development' },
]

/**
 * How many recently-opened clusters are drawn. Three, because the block is a
 * shortcut and a fourth row starts being the list below it drawn twice.
 */
const RECENT_SHOWN = 3

/**
 * The namespaces a row's grant covers. This is a fact about the *grant*, not
 * about the cluster, so it is answered the same way whether or not the cluster
 * can be reached right now — a cluster being down does not narrow what somebody
 * was given, and overwriting it with an error would hide the one thing this
 * column exists to say while repeating what the glyph and the name already do.
 */
function namespaceReading(cluster: Cluster): string {
  return cluster.namespaces.length === 0 ? 'all namespaces' : cluster.namespaces.join(', ')
}

/**
 * The line under a cluster's name: normally its version, and the cluster's own
 * words for what is wrong when something is. That is where "why can I not open
 * this" belongs — beside the name, rather than in a column about access.
 */
function statusReading(cluster: Cluster): string {
  if (cluster.status === 'unhealthy') {
    return cluster.status_message ?? 'unreachable right now'
  }
  if (cluster.connection_mode === 'agent' && !cluster.agent_connected_at) {
    return 'not dialled in yet'
  }
  return cluster.kubernetes_version ?? 'version not read'
}

/* --------------------------------------------------------------- elevation --- */

/**
 * The countdown on a live elevation. It ticks locally against the server's own
 * `expires_at` rather than re-reading the request every second — the same rule
 * `JitApprovalsPanel` follows, and for the same reason: the deadline is a fact
 * the server already stated, and asking it again every second is a poll nobody
 * asked for.
 */
function ElevationStrip({ request }: { request: JitRequest }) {
  const remaining = useCountdown(request.expires_at ?? null)

  return (
    <section className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-card border border-warn bg-warn-soft px-3.5 py-2.5">
      <KeyRound aria-hidden="true" className="size-4 shrink-0 text-warn" />
      <span className="label text-warn">Elevated</span>
      <span className="text-[13px] text-fg">
        <span className="font-mono font-semibold">{request.requested_role}</span> on{' '}
        <span className="font-mono font-semibold">{request.cluster_name}</span>
        {request.approver_username ? (
          <span className="text-muted"> · approved by {request.approver_username}</span>
        ) : null}
      </span>
      <span className="ml-auto font-mono text-[12.5px] font-semibold text-warn tabular-nums">
        {formatCountdown(remaining)} left
      </span>
    </section>
  )
}

/* ----------------------------------------------------------------- recents --- */

function RecentCluster({ cluster }: { cluster: Cluster }) {
  return (
    <Link
      to={clusterHref(cluster)}
      className="group flex flex-col gap-1.5 rounded-card border border-line bg-surface p-3.5 transition-[border-color,box-shadow] hover:border-accent-line hover:lift"
    >
      <span className="label">Last opened</span>
      <span className="flex min-w-0 items-center gap-2">
        <LinkStatus state={linkState(cluster)} variant="glyph" />
        <span className="truncate font-mono text-[14px] font-semibold text-fg group-hover:text-accent">
          {cluster.name}
        </span>
      </span>
      <span className="truncate font-mono text-[11.5px] text-muted">
        {cluster.k8s_role} · {namespaceReading(cluster)}
      </span>
      {cluster.status === 'unhealthy' ? (
        <span className="truncate text-[11.5px] text-danger">{statusReading(cluster)}</span>
      ) : null}
      <span className="mt-auto inline-flex items-center gap-1 pt-1 text-[12px] font-semibold text-faint group-hover:text-accent">
        {hasTunnel(cluster) ? 'Explore' : 'Open cluster'}
        <ChevronRight aria-hidden="true" className="size-3.5" />
      </span>
    </Link>
  )
}

/* -------------------------------------------------------------------- rows --- */

function ClusterRow({ cluster }: { cluster: Cluster }) {
  const failing = cluster.status === 'unhealthy'

  return (
    <li>
      <Link
        to={clusterHref(cluster)}
        className="group grid grid-cols-[18px_minmax(0,1fr)_auto] items-center gap-x-5 gap-y-2 border-b border-line-soft px-3 py-3.5 first:border-t hover:bg-surface sm:grid-cols-[18px_minmax(180px,300px)_140px_minmax(0,1fr)_auto]"
      >
        <LinkStatus state={linkState(cluster)} variant="glyph" />

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
            title={statusReading(cluster)}
          >
            {statusReading(cluster)}
          </span>
        </span>

        <span className="col-start-2 flex min-w-0 flex-col gap-0.5 sm:col-start-3">
          <span className="label">Your role</span>
          <span className="truncate font-mono text-[12.5px] leading-tight text-fg">
            {cluster.k8s_role}
          </span>
        </span>

        <span className="col-start-2 flex min-w-0 flex-col gap-0.5 sm:col-start-4">
          <span className="label">Namespaces</span>
          <span
            className="truncate font-mono text-[12px] leading-tight text-muted"
            title={namespaceReading(cluster)}
          >
            {namespaceReading(cluster)}
          </span>
        </span>

        <span className="col-start-3 inline-flex items-center gap-1 self-center text-[12px] font-semibold whitespace-nowrap text-faint group-hover:text-accent sm:col-start-5">
          {hasTunnel(cluster) ? 'Explore' : 'Open cluster'}
          <ChevronRight aria-hidden="true" className="size-3.5" />
        </span>
      </Link>
    </li>
  )
}

/* -------------------------------------------------------------------- body --- */

export function FleetDeveloperBody({
  clusters,
  elevation,
  onRequestAccess,
}: {
  clusters: Cluster[]
  /** The caller's own live elevation, when they hold one. */
  elevation: JitRequest | null
  onRequestAccess: () => void
}) {
  const recentIds = useRecentClusters()

  // Recents resolve against the fleet this caller can actually see, so a
  // cluster they have since lost access to simply drops out of the row.
  const recents = useMemo(() => {
    const byId = new Map(clusters.map((cluster) => [cluster.id, cluster]))
    return recentIds
      .map((id) => byId.get(id))
      .filter((cluster): cluster is Cluster => Boolean(cluster))
      .slice(0, RECENT_SHOWN)
  }, [recentIds, clusters])

  const reachable = clusters.filter((cluster) => cluster.status === 'healthy').length
  const environments = BANDS.filter(({ environment }) =>
    clusters.some((cluster) => cluster.environment === environment),
  ).length

  return (
    <>
      <div className="flex flex-wrap items-end gap-4">
        <div>
          <h2 className="text-[22px] leading-tight font-semibold tracking-[-0.03em] text-fg">
            {clusters.length} {clusters.length === 1 ? 'cluster' : 'clusters'} you can open.
          </h2>
          <p className="mt-1 text-[13px] text-muted">
            Across {environments} {environments === 1 ? 'environment' : 'environments'} ·{' '}
            {reachable} reachable right now
          </p>
        </div>
        <div className="ml-auto">
          <Button variant="primary" onClick={onRequestAccess}>
            <KeyRound aria-hidden="true" className="size-4" />
            Request access
          </Button>
        </div>
      </div>

      {elevation ? <ElevationStrip request={elevation} /> : null}

      {/* Recency is only a shortcut once there is something to shortcut past. */}
      {recents.length > 0 && clusters.length > 1 ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {recents.map((cluster) => (
            <RecentCluster key={cluster.id} cluster={cluster} />
          ))}
        </div>
      ) : null}

      {BANDS.map(({ environment, title }) => {
        const band = clusters.filter((cluster) => cluster.environment === environment)
        if (band.length === 0) return null

        return (
          <section key={environment} className="flex flex-col">
            <div className="flex items-center gap-2.5 px-0.5 pb-1.5">
              <h3 className="label text-fg">{title}</h3>
              <EnvironmentTag environment={environment} />
              <span className="h-px flex-1 bg-line" />
              <span className="text-[11px] text-faint">
                {band.length} {band.length === 1 ? 'cluster' : 'clusters'}
              </span>
            </div>
            <ul>
              {band.map((cluster) => (
                <ClusterRow key={cluster.id} cluster={cluster} />
              ))}
            </ul>
          </section>
        )
      })}
    </>
  )
}
