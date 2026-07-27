import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Plug, Plus, RefreshCw, Server } from 'lucide-react'
import { checkCluster, errorMessage } from '../api/client'
import type { Cluster, Environment } from '../api/types'
import { AppShell } from '../components/AppShell'
import { LinkStrand } from '../components/LinkStrand'
import {
  Button,
  ClusterState,
  EmptyState,
  EnvironmentTag,
  Notice,
  SectionHeading,
} from '../components/primitives'
import { strandState } from '../lib/status'
import { relativeAge } from '../lib/time'
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
                    <ClusterCard cluster={cluster} />
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
function ClusterCard({ cluster }: { cluster: Cluster }) {
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
    </Link>
  )
}
