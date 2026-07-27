import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { AlertTriangle, ChevronRight, KeyRound, RefreshCw } from 'lucide-react'
import { checkCluster, errorMessage, fetchCluster } from '../api/client'
import type { Cluster } from '../api/types'
import { AppShell } from '../components/AppShell'
import { KubeconfigDrawer } from '../components/KubeconfigDrawer'
import { Button, EnvironmentTag, Notice, Panel, StatusDot } from '../components/primitives'
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

  return (
    <AppShell
      title={cluster?.name ?? 'Cluster'}
      parent={{ label: 'Fleet overview', to: '/' }}
      actions={
        cluster ? (
          <>
            {user?.role === 'admin' ? (
              <Button onClick={check} disabled={checking}>
                <RefreshCw
                  aria-hidden="true"
                  className={`size-3.5 ${checking ? 'animate-spin' : ''}`}
                />
                {checking ? 'Checking…' : 'Run check'}
              </Button>
            ) : null}
            <Button variant="primary" onClick={() => setDrawerOpen(true)}>
              <KeyRound aria-hidden="true" className="size-3.5" />
              Generate kubeconfig
            </Button>
          </>
        ) : null
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {loading ? <p className="text-[12px] text-muted">Loading…</p> : null}

        {cluster ? (
          <>
            <section className="panel p-3.5">
              <div className="flex flex-wrap items-center gap-2.5">
                <h2 className="font-mono text-[19px] font-semibold tracking-[-0.01em] text-fg">
                  {cluster.name}
                </h2>
                <EnvironmentTag environment={cluster.environment} />
                <StatusDot status={cluster.status} message={cluster.status_message} />
                <span className="ml-auto text-[12px] text-muted">
                  checked {relativeAge(cluster.last_checked_at)}
                </span>
              </div>

              <dl className="mt-3 grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-2 border-t border-line-soft pt-3 text-[12.5px] sm:grid-cols-[110px_minmax(0,1fr)_110px_minmax(0,1fr)]">
                <dt className="text-muted">API server</dt>
                <dd className="truncate font-mono text-fg" title={cluster.api_url}>
                  {cluster.api_url}
                </dd>
                <dt className="text-muted">Kubernetes</dt>
                <dd className="font-mono text-fg">{cluster.kubernetes_version ?? 'unknown'}</dd>
                <dt className="text-muted">Registered</dt>
                <dd className="font-mono text-fg">
                  {new Date(cluster.created_at).toLocaleString()}
                </dd>
                <dt className="text-muted">Namespaces</dt>
                <dd className="truncate font-mono text-fg">
                  {cluster.namespaces.length > 0 ? cluster.namespaces.join(', ') : 'all'}
                </dd>
              </dl>
            </section>

            {cluster.status === 'unhealthy' && cluster.status_message ? (
              <Notice tone="error">{cluster.status_message}</Notice>
            ) : null}

            <AccessPath cluster={cluster} username={user?.username ?? 'you'} />

            {cluster.connection_mode === 'agent' ? (
              <Panel title="How this cluster is reached">
                <p className="p-3.5 text-[12.5px] leading-relaxed text-muted">
                  An agent inside this cluster holds an outbound tunnel to KubeMG, and every
                  proxied call is replayed under your own identity using Kubernetes impersonation.
                  The cluster&rsquo;s RBAC decides what that identity may do — the grant above
                  decides which cluster and namespaces KubeMG will carry you to. Every call is
                  written to the audit log.
                </p>
              </Panel>
            ) : (
              <Panel title="What a kubeconfig for this cluster does">
                <p className="p-3.5 text-[12.5px] leading-relaxed text-muted">
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
 * AccessPath is the chain that decides what a kubeconfig for this cluster can
 * do: who you are, what KubeMG granted you, and where that grant stops. The
 * last hop is amber on purpose — no RoleBinding is created in the cluster, and
 * the UI should say so where the decision is made, not in a footnote.
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
    <section>
      <div className="mb-2.5 flex items-center gap-2.5">
        <h2 className="text-[13px] font-semibold text-fg">How your access is derived</h2>
        <span aria-hidden="true" className="h-px flex-1 bg-line" />
      </div>

      <ol className="panel flex flex-col overflow-hidden sm:flex-row">
        {hops.map((hop, index) => (
          <li
            key={hop.label}
            className={`relative flex min-w-0 flex-1 flex-col gap-0.5 border-b border-line-soft px-3.5 py-2.5 last:border-b-0 sm:border-r sm:border-b-0 sm:last:border-r-0 ${
              hop.gap ? 'bg-warn-soft' : ''
            }`}
          >
            <span className="label">{hop.label}</span>
            <span
              className={`flex items-center gap-1.5 truncate font-mono text-[13px] ${
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
                className="absolute top-1/2 right-0 hidden size-4 -translate-y-1/2 translate-x-1/2 rounded-full bg-surface text-faint sm:block"
              />
            ) : null}
          </li>
        ))}
      </ol>
    </section>
  )
}
