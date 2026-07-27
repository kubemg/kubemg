import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { AlertTriangle, ChevronRight, KeyRound, RefreshCw } from 'lucide-react'
import { checkCluster, errorMessage, fetchCluster } from '../api/client'
import type { Cluster } from '../api/types'
import { AppShell } from '../components/AppShell'
import { KubeconfigDrawer } from '../components/KubeconfigDrawer'
import { LinkStrand, StrandNode } from '../components/LinkStrand'
import {
  Button,
  ClusterState,
  DetailList,
  EnvironmentTag,
  Notice,
  Panel,
} from '../components/primitives'
import { strandState } from '../lib/status'
import { relativeAge } from '../lib/time'
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
