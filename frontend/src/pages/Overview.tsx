import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Plug, Plus, RefreshCw, Server } from 'lucide-react'
import { checkCluster, errorMessage, fetchNodeMetrics } from '../api/client'
import type { Cluster, Environment, UsageSummary } from '../api/types'
import { AppShell } from '../components/AppShell'
import { LinkStrand } from '../components/LinkStrand'
import {
  Button,
  ClusterState,
  EmptyState,
  EnvironmentTag,
  Meter,
  Notice,
  SectionHeading,
} from '../components/primitives'
import { strandState } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory, ratio } from '../lib/units'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

/* Bands run prod first: the fleet is read top-down by how much a cluster matters. */
const BANDS: Array<{ environment: Environment; title: string }> = [
  { environment: 'prod', title: 'Production' },
  { environment: 'staging', title: 'Staging' },
  { environment: 'dev', title: 'Development' },
]

export function Overview() {
  const { clusters, loading, error, reload } = useClusters()
  const { user } = useAuth()
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)

  const capacity = useFleetCapacity(clusters)

  const isAdmin = user?.role === 'admin'
  const reachable = clusters.filter((cluster) => cluster.status === 'healthy').length
  const unreachable = clusters.filter((cluster) => cluster.status === 'unhealthy').length
  const tunnels = clusters.filter((cluster) => cluster.agent_attached).length
  const lastChecked = clusters
    .map((cluster) => cluster.last_checked_at)
    .filter((value): value is string => Boolean(value))
    .sort()
    .at(-1)

  async function checkAll() {
    setChecking(true)
    setCheckError(null)
    try {
      const results = await Promise.allSettled(clusters.map((cluster) => checkCluster(cluster.id)))
      const failed = results.find((result) => result.status === 'rejected')
      if (failed && failed.status === 'rejected') {
        setCheckError(errorMessage(failed.reason, 'Some clusters could not be checked.'))
      }
      await reload()
    } finally {
      setChecking(false)
    }
  }

  return (
    <AppShell
      title="Fleet"
      actions={
        isAdmin && clusters.length > 0 ? (
          <Button variant="primary" onClick={checkAll} disabled={checking}>
            <RefreshCw aria-hidden="true" className={`size-4 ${checking ? 'animate-spin' : ''}`} />
            {checking ? 'Checking…' : 'Check every cluster'}
          </Button>
        ) : null
      }
    >
      <div className="flex flex-col gap-6">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {checkError ? <Notice tone="error">{checkError}</Notice> : null}

        {clusters.length > 0 ? (
          <div className="grid gap-3 sm:grid-cols-3">
            <Stat
              label="Reachable"
              value={`${reachable}/${clusters.length}`}
              detail={unreachable > 0 ? `${unreachable} unreachable` : 'nothing failing'}
              tone={unreachable > 0 ? 'bad' : 'ok'}
            />
            <Stat
              label="Tunnels open"
              value={String(tunnels)}
              detail={`of ${clusters.filter((c) => c.connection_mode === 'agent').length} agent clusters`}
              tone={tunnels > 0 ? 'ok' : 'idle'}
            />
            <Stat
              label="Last sweep"
              value={relativeAge(lastChecked)}
              detail={isAdmin ? 'health is checked on demand' : 'checked by an administrator'}
              tone="idle"
            />
          </div>
        ) : null}

        <FleetCapacity capacity={capacity} />

        {loading && clusters.length === 0 ? (
          <p className="text-[13px] text-muted">Loading the fleet…</p>
        ) : null}

        {!loading && clusters.length === 0 ? (
          <div className="card">
            <EmptyState
              icon={<Server aria-hidden="true" className="size-5" />}
              title="No clusters yet"
              action={
                isAdmin ? (
                  <Link to="/clusters/new">
                    <Button variant="primary">
                      <Plus aria-hidden="true" className="size-4" />
                      Register a cluster
                    </Button>
                  </Link>
                ) : null
              }
            >
              {isAdmin
                ? 'Register one to bring it under KubeMG. The cluster dials out to here, so nothing needs to be opened inbound.'
                : 'Ask an administrator for access to a cluster.'}
            </EmptyState>
          </div>
        ) : null}

        {BANDS.map(({ environment, title }) => {
          const band = clusters.filter((cluster) => cluster.environment === environment)
          if (band.length === 0) return null

          const failing = band.filter((cluster) => cluster.status === 'unhealthy').length

          return (
            <section key={environment} className="flex flex-col gap-3">
              <SectionHeading
                title={title}
                meta={
                  <>
                    {band.length} {band.length === 1 ? 'cluster' : 'clusters'}
                    {failing > 0 ? <span className="text-danger"> · {failing} failing</span> : null}
                  </>
                }
              >
                <EnvironmentTag environment={environment} />
              </SectionHeading>

              <ul className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
                {band.map((cluster) => (
                  <li key={cluster.id}>
                    <ClusterCard cluster={cluster} usage={capacity.byCluster[cluster.id]} />
                  </li>
                ))}
              </ul>
            </section>
          )
        })}
      </div>
    </AppShell>
  )
}

/*
 * Fleet capacity is a fan-out: every cluster's usage is a separate read down a
 * separate tunnel, so this is the one place in the app where the cost scales
 * with fleet size. It is capped rather than paginated — past a dozen clusters
 * the honest answer is that this page is not the right place to ask, and the
 * cluster pages are — and only attached agent clusters are asked at all, since
 * anything else has nothing to answer with.
 */
const FLEET_METRICS_LIMIT = 12

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

/** FleetCapacity is the fleet's headroom, summed across everything it could read. */
function FleetCapacity({ capacity }: { capacity: FleetCapacityState }) {
  const { total, skipped, reading } = capacity
  if (!total && !reading) return null

  return (
    <section className="card px-4 py-3.5">
      <div className="flex flex-wrap items-baseline gap-2">
        <p className="label">Fleet capacity</p>
        <p className="text-[12px] text-muted">
          {total
            ? `${total.nodes} ${total.nodes === 1 ? 'node' : 'nodes'} across the clusters serving metrics`
            : 'Reading…'}
        </p>
        {skipped > 0 ? (
          <p className="ml-auto text-[12px] text-faint">
            {skipped} more not read — open a cluster for its own numbers
          </p>
        ) : null}
      </div>

      {total ? (
        <div className="mt-3 grid gap-4 sm:grid-cols-2">
          <Meter
            label="CPU"
            value={formatCPU(total.cpu_millicores)}
            percent={ratio(total.cpu_millicores, total.cpu_capacity_millicores)}
            capacity={formatCPU(total.cpu_capacity_millicores)}
          />
          <Meter
            label="Memory"
            value={formatMemory(total.memory_bytes)}
            percent={ratio(total.memory_bytes, total.memory_capacity_bytes)}
            capacity={formatMemory(total.memory_capacity_bytes)}
          />
        </div>
      ) : null}
    </section>
  )
}

function Stat({
  label,
  value,
  detail,
  tone,
}: {
  label: string
  value: string
  detail: string
  tone: 'ok' | 'bad' | 'idle'
}) {
  const accent = tone === 'bad' ? 'text-danger' : 'text-fg'

  return (
    <div className="card px-4 py-3.5">
      <p className="label">{label}</p>
      <p className={`mt-1 font-mono text-[26px] leading-none font-semibold ${accent}`}>{value}</p>
      <p className="mt-2 text-[12px] text-muted">{detail}</p>
    </div>
  )
}

/**
 * ClusterCard leads with the link, not with the metadata: whether KubeMG can
 * reach a cluster right now is the only thing that decides if anything else on
 * the card matters.
 */
function ClusterCard({ cluster, usage }: { cluster: Cluster; usage?: UsageSummary }) {
  const failing = cluster.status === 'unhealthy'
  const viaAgent = cluster.connection_mode === 'agent'

  return (
    <Link
      to={`/clusters/${cluster.id}`}
      className="group card block p-4 transition-[border-color,box-shadow] hover:border-accent-line hover:lift"
    >
      <div className="flex items-center gap-2.5">
        <span className="min-w-0 flex-1 truncate font-mono text-[15px] font-semibold text-fg">
          {cluster.name}
        </span>
        <ClusterState cluster={cluster} />
      </div>

      <div className="mt-3 flex items-center gap-2.5">
        <span className="shrink-0 text-faint">
          {viaAgent ? (
            <Plug aria-hidden="true" className="size-3.5" />
          ) : (
            <Server aria-hidden="true" className="size-3.5" />
          )}
        </span>
        <LinkStrand state={strandState(cluster)} className="flex-1" />
        <span className="shrink-0 font-mono text-[11px] text-faint">
          {viaAgent ? (cluster.agent_attached ? 'tunnel up' : 'no tunnel') : 'direct'}
        </span>
      </div>

      <dl className="mt-4 grid grid-cols-2 gap-3 border-t border-line-soft pt-3">
        <div className="min-w-0">
          <dt className="label">Kubernetes</dt>
          <dd className="mt-0.5 truncate font-mono text-[13px] text-fg">
            {cluster.kubernetes_version ?? '—'}
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="label">Your access</dt>
          <dd className="mt-0.5 truncate font-mono text-[13px] text-fg">
            {cluster.k8s_role}
            {cluster.namespaces.length > 0 ? (
              <span className="text-muted"> · {cluster.namespaces.length} ns</span>
            ) : null}
          </dd>
        </div>
        <div className="col-span-2 min-w-0">
          <dt className="label">{failing ? 'Last error' : 'Last check'}</dt>
          <dd
            className={`mt-0.5 truncate font-mono text-[13px] ${
              failing ? 'text-danger' : cluster.status === 'pending' ? 'text-muted' : 'text-fg'
            }`}
            title={cluster.status_message}
          >
            {failing
              ? (cluster.status_message ?? 'Unreachable')
              : cluster.status === 'pending'
                ? 'never checked'
                : relativeAge(cluster.last_checked_at)}
          </dd>
        </div>
      </dl>

      {/* Only the clusters the fan-out reached have this; a card without it is
          a cluster with no tunnel or no metrics-server, not a broken card. */}
      {usage ? (
        <div className="mt-3 grid gap-3 border-t border-line-soft pt-3 sm:grid-cols-2">
          <Meter
            label="CPU"
            value={formatCPU(usage.cpu_millicores)}
            percent={usage.cpu_percent}
            capacity={formatCPU(usage.cpu_capacity_millicores)}
          />
          <Meter
            label="Memory"
            value={formatMemory(usage.memory_bytes)}
            percent={usage.memory_percent}
            capacity={formatMemory(usage.memory_capacity_bytes)}
          />
        </div>
      ) : null}
    </Link>
  )
}
