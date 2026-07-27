import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { AlertTriangle, ChevronRight, KeyRound, RefreshCw } from 'lucide-react'
import { checkCluster, errorMessage, fetchCluster, fetchNodeMetrics } from '../api/client'
import type { Cluster, NodeMetrics } from '../api/types'
import { AppShell } from '../components/AppShell'
import { DatasourcePanel } from '../components/DatasourcePanel'
import { KubeconfigDrawer } from '../components/KubeconfigDrawer'
import { LinkStrand, StrandNode } from '../components/LinkStrand'
import {
  Button,
  ClusterState,
  DetailList,
  EnvironmentTag,
  Meter,
  Notice,
  Panel,
} from '../components/primitives'
import { strandState } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory } from '../lib/units'
import { useAuth } from '../state/auth-context'

export function ClusterDetail() {
  const { id } = useParams<{ id: string }>()
  const { user } = useAuth()
  const [cluster, setCluster] = useState<Cluster | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [checking, setChecking] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)

  const clusterId = Number(id)

  const load = useCallback(async () => {
    if (!Number.isFinite(clusterId)) {
      setError('That cluster id is not valid.')
      setLoading(false)
      return
    }
    try {
      setCluster(await fetchCluster(clusterId))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load this cluster.'))
    } finally {
      setLoading(false)
    }
  }, [clusterId])

  useEffect(() => {
    void load()
  }, [load])

  async function check() {
    setChecking(true)
    try {
      setCluster(await checkCluster(clusterId))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not check this cluster.'))
    } finally {
      setChecking(false)
    }
  }

  const viaAgent = cluster?.connection_mode === 'agent'

  return (
    <AppShell
      title={cluster?.name ?? 'Cluster'}
      parent={{ label: 'Fleet', to: '/' }}
      actions={
        cluster ? (
          <>
            {user?.role === 'admin' ? (
              <Button onClick={check} disabled={checking}>
                <RefreshCw
                  aria-hidden="true"
                  className={`size-4 ${checking ? 'animate-spin' : ''}`}
                />
                {checking ? 'Checking…' : 'Run check'}
              </Button>
            ) : null}
            <Button variant="primary" onClick={() => setDrawerOpen(true)}>
              <KeyRound aria-hidden="true" className="size-4" />
              Generate kubeconfig
            </Button>
          </>
        ) : null
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {cluster ? (
          <>
            <section className="card p-5">
              <div className="flex flex-wrap items-center gap-3">
                <h2 className="font-mono text-[22px] font-semibold tracking-[-0.01em] text-fg">
                  {cluster.name}
                </h2>
                <EnvironmentTag environment={cluster.environment} />
                <ClusterState cluster={cluster} />
                <span className="ml-auto text-[12.5px] text-muted">
                  checked {relativeAge(cluster.last_checked_at)}
                </span>
              </div>

              {cluster.description ? (
                <p className="mt-2 max-w-2xl text-[13px] leading-relaxed text-muted">
                  {cluster.description}
                </p>
              ) : null}

              {/* The path traffic actually takes, drawn once, at the top of the
                  cluster it belongs to. */}
              <div className="mt-5 flex flex-col gap-3 rounded-card border border-line-soft bg-raised/50 p-4 sm:flex-row sm:items-end sm:gap-5">
                <StrandNode
                  label="Cluster"
                  value={cluster.name}
                  tone={cluster.status === 'healthy' ? 'ok' : 'idle'}
                />
                <span className="min-w-16 flex-1 pb-2">
                  <LinkStrand state={strandState(cluster)} size="lg" />
                  <span className="mt-1.5 block font-mono text-[11px] text-faint">
                    {viaAgent
                      ? cluster.agent_attached
                        ? 'outbound tunnel · open'
                        : 'outbound tunnel · not connected'
                      : 'KubeMG dials the API server'}
                  </span>
                </span>
                <StrandNode
                  label="KubeMG"
                  value={viaAgent ? 'bastion proxy' : 'token issuer'}
                  tone="accent"
                />
                <span className="min-w-16 flex-1 pb-2">
                  <LinkStrand state={viaAgent ? 'live' : 'direct'} size="lg" />
                  <span className="mt-1.5 block font-mono text-[11px] text-faint">
                    {viaAgent ? 'proxied · audited' : 'kubeconfig · not proxied'}
                  </span>
                </span>
                <StrandNode label="You" value={user?.username ?? 'you'} />
              </div>

              <div className="mt-5 border-t border-line-soft pt-4">
                <DetailList
                  columns={2}
                  rows={[
                    { term: 'API server', value: cluster.api_url || 'via agent tunnel' },
                    { term: 'Kubernetes', value: cluster.kubernetes_version ?? 'unknown' },
                    {
                      term: viaAgent ? 'Agent' : 'Connection',
                      value: viaAgent
                        ? (cluster.agent_version ?? 'not seen yet')
                        : 'direct API access',
                    },
                    {
                      term: 'Registered',
                      value: new Date(cluster.created_at).toLocaleString(),
                    },
                  ]}
                />
              </div>
            </section>

            {cluster.status === 'unhealthy' && cluster.status_message ? (
              <Notice tone="error">{cluster.status_message}</Notice>
            ) : null}

            {/* Capacity only exists for a cluster KubeMG can actually read
                through, which is the agent path. */}
            {viaAgent ? <Capacity cluster={cluster} /> : null}

            {/* Capacity above is a live sample and nothing more; this is where
                the history behind it comes from, wired per cluster. */}
            <DatasourcePanel cluster={cluster} />

            <AccessPath cluster={cluster} username={user?.username ?? 'you'} />

            {viaAgent ? (
              <Panel
                title="How this cluster is reached"
                eyebrow="Agent mode"
                bodyClassName="p-4"
              >
                <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
                  An agent inside this cluster holds an outbound tunnel to KubeMG, and every proxied
                  call is replayed under your own identity using Kubernetes impersonation. The
                  cluster&rsquo;s own RBAC decides what that identity may do — the grant above decides
                  which cluster and namespaces KubeMG will carry you to. Every call is written to the
                  audit trail.
                </p>
              </Panel>
            ) : (
              <Panel
                title="What a kubeconfig for this cluster does"
                eyebrow="Direct mode"
                bodyClassName="p-4"
              >
                <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
                  KubeMG issues a short-lived token for this cluster&rsquo;s KubeMG service account
                  through the Kubernetes TokenRequest API. It creates no RoleBinding, so the grant
                  above decides what you see in KubeMG — not what the cluster lets you do. Register
                  the cluster in agent mode to have KubeMG bind these roles for real.
                </p>
              </Panel>
            )}
          </>
        ) : null}
      </div>

      {drawerOpen && cluster ? (
        <KubeconfigDrawer cluster={cluster} onClose={() => setDrawerOpen(false)} />
      ) : null}
    </AppShell>
  )
}

/**
 * Capacity is what the cluster is actually using, read from its own Metrics
 * API through the same audited tunnel as everything else. It leads with the
 * cluster total, because the first question is whether the cluster has room;
 * the per-node rows underneath answer the second one, which is whether that
 * room is where the work is.
 *
 * There is no chart here on purpose: metrics-server keeps a sliding window of
 * a couple of minutes, so there is no series to draw and pretending otherwise
 * would invent history the cluster does not have.
 */
function Capacity({ cluster }: { cluster: Cluster }) {
  const [metrics, setMetrics] = useState<NodeMetrics | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let live = true
    async function read() {
      try {
        const next = await fetchNodeMetrics(cluster.id)
        if (!live) return
        setMetrics(next)
        setError(null)
      } catch (err) {
        if (!live) return
        setError(errorMessage(err, 'Could not read this cluster’s usage.'))
      } finally {
        if (live) setLoading(false)
      }
    }

    void read()
    // metrics-server samples every 15s or so; matching it keeps the panel live
    // without spending tunnel round trips on numbers that have not moved.
    const timer = window.setInterval(() => void read(), 15_000)
    return () => {
      live = false
      window.clearInterval(timer)
    }
  }, [cluster.id])

  const summary = metrics?.summary

  return (
    <Panel
      title="Capacity"
      eyebrow="Live"
      description="Current consumption against allocatable capacity, read from the cluster's Metrics API."
      bodyClassName="flex flex-col gap-4 p-4"
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {!error && metrics && !metrics.available ? (
        <Notice tone="info">{metrics.reason}</Notice>
      ) : null}
      {loading && !metrics ? <p className="text-[13px] text-muted">Reading usage…</p> : null}

      {metrics?.available && summary ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Meter
              label={`CPU across ${summary.nodes} ${summary.nodes === 1 ? 'node' : 'nodes'}`}
              value={formatCPU(summary.cpu_millicores)}
              percent={summary.cpu_percent}
              capacity={formatCPU(summary.cpu_capacity_millicores)}
            />
            <Meter
              label="Memory"
              value={formatMemory(summary.memory_bytes)}
              percent={summary.memory_percent}
              capacity={formatMemory(summary.memory_capacity_bytes)}
            />
          </div>

          <ul className="flex flex-col gap-3 border-t border-line-soft pt-4">
            {metrics.nodes.map((node) => (
              <li key={node.name} className="flex flex-col gap-2">
                <span className="truncate font-mono text-[13px] text-fg" title={node.name}>
                  {node.name}
                </span>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Meter
                    label="CPU"
                    value={formatCPU(node.cpu_millicores)}
                    percent={node.cpu_percent}
                    capacity={formatCPU(node.cpu_capacity_millicores)}
                  />
                  <Meter
                    label="Memory"
                    value={formatMemory(node.memory_bytes)}
                    percent={node.memory_percent}
                    capacity={formatMemory(node.memory_capacity_bytes)}
                  />
                </div>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </Panel>
  )
}

/**
 * AccessPath is the chain that decides what access to this cluster can do: who
 * you are, what KubeMG granted you, and where that grant stops. In direct mode
 * the last hop is amber on purpose — no RoleBinding is created in the cluster,
 * and the UI says so where the decision is made, not in a footnote.
 */
function AccessPath({ cluster, username }: { cluster: Cluster; username: string }) {
  // An agent cluster closes the chain: the installed manifests bind the
  // kubemg:* groups to real ClusterRoles, so the last hop is no longer a gap.
  const viaAgent = cluster.connection_mode === 'agent'

  const hops = [
    { label: 'Identity', value: username, gap: false },
    { label: 'Grant in KubeMG', value: cluster.k8s_role, gap: false },
    {
      label: 'Namespaces',
      value: cluster.namespaces.length > 0 ? cluster.namespaces.join(', ') : 'all',
      gap: false,
    },
    viaAgent
      ? { label: 'Cluster RBAC', value: `kubemg:${cluster.k8s_role}`, gap: false }
      : { label: 'Cluster RBAC', value: 'no RoleBinding', gap: true },
  ]

  return (
    <Panel title="How your access is derived" eyebrow="Chain">
      <ol className="flex flex-col md:flex-row">
        {hops.map((hop, index) => (
          <li
            key={hop.label}
            className={`relative flex min-w-0 flex-1 flex-col gap-1 border-b border-line-soft px-4 py-3 last:border-b-0 md:border-r md:border-b-0 md:last:border-r-0 ${
              hop.gap ? 'bg-warn-soft' : ''
            }`}
          >
            <span className="label">{hop.label}</span>
            <span
              className={`flex items-center gap-1.5 truncate font-mono text-[13.5px] ${
                hop.gap ? 'text-warn' : 'text-fg'
              }`}
              title={hop.value}
            >
              {hop.gap ? <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" /> : null}
              {hop.value}
            </span>
            {index < hops.length - 1 ? (
              <ChevronRight
                aria-hidden="true"
                className="absolute top-1/2 right-0 hidden size-4 -translate-y-1/2 translate-x-1/2 rounded-full bg-surface text-faint md:block"
              />
            ) : null}
          </li>
        ))}
      </ol>
    </Panel>
  )
}
