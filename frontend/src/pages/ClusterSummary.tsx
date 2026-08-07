import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { AlertTriangle, ChevronRight, KeyRound, Layers, RefreshCw, Timer } from 'lucide-react'
import { checkCluster, errorMessage, fetchCluster, fetchNodeMetrics } from '../api/client'
import type { Cluster, NodeMetrics } from '../api/types'
import { AppShell } from '../components/AppShell'
import { ConsolesPanel } from '../components/ConsolesPanel'
import { DatasourcePanel } from '../components/DatasourcePanel'
import { MetricComparison } from '../components/MetricComparison'
import type { ComparisonKind } from '../components/MetricComparison'
import { MetricsChart } from '../components/MetricsChart'
import { JitRequestModal } from '../components/jit/JitRequestModal'
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
import { CardSkeleton, MeterGridSkeleton } from '../components/SkeletonLoader'
import { queryKey, useCachedQuery } from '../lib/query'
import { strandState } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory } from '../lib/units'
import { useAuth } from '../state/auth-context'

/*
 * What a cluster page ranks. Two readings of what it costs, three of what is
 * going wrong — and nothing else, because the pattern this is drawn from also
 * carries response time, throughput and error rate, which come from APM agent
 * instrumentation KubeMG does not collect. A column that would always read "no
 * data" is worse than one that is not there.
 *
 * CPU and memory break down per namespace rather than per pod: the top five pods
 * out of several thousand is a list of five strangers, while the top five
 * namespaces is the vocabulary the fleet is already organised by. The failure
 * readings stay per pod, because a pod is the thing that restarts.
 */
const CLUSTER_READINGS: ComparisonKind[] = [
  { kind: 'cluster_cpu_by_namespace', label: 'CPU' },
  { kind: 'cluster_memory_by_namespace', label: 'Memory' },
  { kind: 'pod_restarts', label: 'Restarts' },
  { kind: 'containers_not_ready', label: 'Not ready' },
  { kind: 'cpu_throttling', label: 'Throttled' },
]

export function ClusterSummary() {
  const { id } = useParams<{ id: string }>()
  const { user } = useAuth()
  const navigate = useNavigate()
  // A health check answers with the cluster as it now is, and that answer is
  // newer than anything cached — so it wins until the page moves to another
  // cluster.
  const [checked, setChecked] = useState<Cluster | null>(null)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  // Asking for more access belongs on the cluster it is about: this page is where
  // somebody has just read what their grant is and found it is not enough.
  const [requesting, setRequesting] = useState(false)
  const [requested, setRequested] = useState(false)

  const clusterId = Number(id)
  const valid = Number.isFinite(clusterId)

  // Read through the query cache: coming back here from Explore or the fleet
  // list inside the window draws the cluster immediately instead of spending a
  // round trip on facts that have not moved.
  const query = useCachedQuery<Cluster>(valid ? queryKey('cluster', clusterId) : null, () =>
    fetchCluster(clusterId),
  )

  useEffect(() => {
    setChecked(null)
    setCheckError(null)
  }, [clusterId])

  const cluster = checked ?? query.data
  const error = !valid
    ? 'That cluster id is not valid.'
    : (checkError ?? (query.error ? errorMessage(query.error, 'Could not load this cluster.') : null))

  async function check() {
    setChecking(true)
    try {
      setChecked(await checkCluster(clusterId))
      setCheckError(null)
    } catch (err) {
      setCheckError(errorMessage(err, 'Could not check this cluster.'))
    } finally {
      setChecking(false)
    }
  }

  const viaAgent = cluster?.connection_mode === 'agent'

  return (
    // The cluster's name is the switcher beside this, so the heading names the
    // view instead — the way every other cluster page's does.
    <AppShell
      title="Summary"
      timeRange
      actions={
        cluster ? (
          <>
            {viaAgent && cluster.agent_attached ? (
              <Button
                variant="primary"
                onClick={() => navigate(`/clusters/${cluster.id}/explore`)}
              >
                <Layers aria-hidden="true" className="size-4" />
                Explore
              </Button>
            ) : null}
            <Button onClick={() => setRequesting(true)}>
              <Timer aria-hidden="true" className="size-4" />
              Request access
            </Button>
            {user?.role === 'admin' ? (
              <Button onClick={check} disabled={checking}>
                <RefreshCw
                  aria-hidden="true"
                  className={`size-4 ${checking ? 'animate-spin' : ''}`}
                />
                {checking ? 'Checking…' : 'Run check'}
              </Button>
            ) : null}
            <Button
              variant={viaAgent && cluster.agent_attached ? undefined : 'primary'}
              onClick={() => setDrawerOpen(true)}
            >
              <KeyRound aria-hidden="true" className="size-4" />
              Generate kubeconfig
            </Button>
          </>
        ) : null
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {/* Where the request went, and where the answer will appear. Without this
            the form closes and nothing visibly happened. */}
        {requested ? (
          <Notice tone="ok">
            Request submitted. It is waiting for an approver — follow it on{' '}
            <Link to="/access-requests" className="text-accent hover:underline">
              access requests
            </Link>
            , where an approved elevation shows its countdown.
          </Notice>
        ) : null}
        {/* The card that is coming, at the size it will be: this page opens with
            a header, a strand and a four-row detail list, and drawing that shape
            keeps the whole page from shifting when the cluster arrives. */}
        {query.loading ? <CardSkeleton lines={4} label="Loading this cluster" /> : null}

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

            {/* And where the questions this console does not answer are
                answered. It sits under the datasource because that is what most
                of it is derived from — and it is a link rather than an embed on
                purpose: KubeMG stores no session for another tool. */}
            <ConsolesPanel cluster={cluster} />

            {/* And this is that history, once there is somewhere to read it
                from. It sits directly under the datasource that answers it, so
                a chart that says "no datasource" is next to the form that fixes
                that rather than on some other page. */}
            {viaAgent ? (
              <section className="flex flex-col gap-3">
                <MetricsChart
                  cluster={cluster}
                  title="Cluster CPU"
                  metric="cluster_cpu"
                />
                <MetricsChart
                  cluster={cluster}
                  title="Cluster memory"
                  metric="cluster_memory"
                />
                {/* The charts say what shape the cluster is in. This says what
                    is worst inside it and whether that is new, which is the
                    question somebody opening a cluster page arrived with — and
                    it is a table because reading a rank off a chart with forty
                    lines is not reading. */}
                <MetricComparison cluster={cluster} kinds={CLUSTER_READINGS} />
              </section>
            ) : null}

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

      {requesting && cluster ? (
        <JitRequestModal
          cluster={cluster}
          onClose={() => setRequesting(false)}
          onCreated={() => {
            setRequesting(false)
            setRequested(true)
          }}
        />
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
      {/* Two meters, at the height two meters occupy: the panel does not grow
          when the first sample lands. */}
      {loading && !metrics ? <MeterGridSkeleton count={2} /> : null}

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
