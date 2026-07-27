import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Plug, Plus, RefreshCw, Search, Server, Trash2 } from 'lucide-react'
import { checkCluster, deleteCluster, errorMessage } from '../api/client'
import type { Cluster } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, EnvironmentTag, Notice, StatusDot } from '../components/primitives'
import { SPINE_TONE } from '../lib/status'
import { useClusters } from '../state/clusters-context'

export function ClusterManagement() {
  const { clusters, loading, error: listError, reload } = useClusters()
  const [rowError, setRowError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)
  const [filter, setFilter] = useState('')
  const [checking, setChecking] = useState<number | null>(null)

  async function check(cluster: Cluster) {
    setChecking(cluster.id)
    setRowError(null)
    try {
      await checkCluster(cluster.id)
      await reload()
    } catch (err) {
      setRowError(errorMessage(err, `Could not check ${cluster.name}.`))
    } finally {
      setChecking(null)
    }
  }

  const needle = filter.trim().toLowerCase()
  const visible = needle
    ? clusters.filter((cluster) => cluster.name.toLowerCase().includes(needle))
    : clusters

  async function remove(cluster: Cluster) {
    const confirmed = window.confirm(
      `Remove ${cluster.name}? Kubeconfigs already issued keep working until they expire.`,
    )
    if (!confirmed) return

    setRemoving(cluster.id)
    setRowError(null)
    try {
      await deleteCluster(cluster.id)
      await reload()
    } catch (err) {
      setRowError(errorMessage(err, `Could not remove ${cluster.name}.`))
    } finally {
      setRemoving(null)
    }
  }

  return (
    <AppShell
      title="Clusters"
      actions={
        <Link to="/clusters/new">
          <Button variant="primary">
            <Plus aria-hidden="true" className="size-3.5" />
            Add cluster
          </Button>
        </Link>
      }
    >
      <div className="flex min-w-0 flex-col gap-3">
        {listError ? <Notice tone="error">{listError}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        <div className="panel min-w-0 overflow-hidden">
          <div className="flex min-h-11 items-center gap-3 border-b border-line-soft px-3">
            <div className="relative">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-faint"
              />
              <input
                type="search"
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
                placeholder="Filter by name"
                aria-label="Filter clusters by name"
                className="w-48 rounded-[5px] border border-line bg-surface py-1 pr-2 pl-7 text-[12px] text-fg transition-colors placeholder:text-faint hover:border-faint focus:border-primary focus:outline-none"
              />
            </div>
            <span className="ml-auto text-[12px] text-muted">
              {visible.length === clusters.length
                ? `${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'}`
                : `${visible.length} of ${clusters.length}`}
            </span>
          </div>

          <div className="overflow-x-auto">
            {/* Narrow screens drop the two widest, least scannable columns
                rather than forcing the whole page to scroll sideways. */}
            <table className="w-full table-fixed border-collapse text-[13px]">
              <thead>
                <tr className="border-b border-line">
                  <th className="w-[3px] p-0">
                    <span className="sr-only">State</span>
                  </th>
                  <th className="label w-[38%] px-3 py-2 text-left md:w-[20%]">Name</th>
                  <th className="label w-[26%] px-3 py-2 text-left md:w-[11%]">Environment</th>
                  <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[10%]">
                    Connection
                  </th>
                  <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[26%]">
                    API server
                  </th>
                  <th className="label w-[24%] px-3 py-2 text-left md:w-[13%]">Status</th>
                  <th className="label hidden px-3 py-2 text-left md:table-cell md:w-[12%]">
                    Version
                  </th>
                  <th className="label w-[12%] px-3 py-2 text-right md:w-[8%]">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {visible.map((cluster) => (
                  <tr
                    key={cluster.id}
                    className="border-b border-line-soft transition-colors last:border-0 hover:bg-raised"
                  >
                    <td className="p-0">
                      <span
                        aria-hidden="true"
                        className={`block h-8 w-[3px] rounded-r-[2px] ${SPINE_TONE[cluster.status]}`}
                      />
                    </td>
                    <td className="truncate px-3 py-2">
                      <Link
                        to={`/clusters/${cluster.id}`}
                        className="font-mono text-fg transition-colors hover:text-primary"
                      >
                        {cluster.name}
                      </Link>
                    </td>
                    <td className="px-3 py-2">
                      <EnvironmentTag environment={cluster.environment} />
                    </td>
                    <td className="hidden px-3 py-2 md:table-cell">
                      <ConnectionTag cluster={cluster} />
                    </td>
                    <td
                      className="hidden truncate px-3 py-2 font-mono text-[12px] text-muted md:table-cell"
                      title={cluster.api_url}
                    >
                      {/* An agent cluster has no API URL here on purpose: KubeMG
                          never learns one, it just answers the tunnel. */}
                      {cluster.api_url || 'via agent tunnel'}
                    </td>
                    <td className="px-3 py-2">
                      <StatusDot status={cluster.status} message={cluster.status_message} />
                    </td>
                    <td className="hidden truncate px-3 py-2 font-mono text-[12px] text-muted md:table-cell">
                      {cluster.kubernetes_version ?? '—'}
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex items-center justify-end gap-0.5">
                        <button
                          type="button"
                          onClick={() => check(cluster)}
                          disabled={checking === cluster.id}
                          className="rounded-sm border border-transparent p-1 text-muted transition-colors hover:border-primary/40 hover:text-primary disabled:opacity-50"
                          title={`Check ${cluster.name}`}
                        >
                          <RefreshCw
                            aria-hidden="true"
                            className={`size-3.5 ${checking === cluster.id ? 'animate-spin' : ''}`}
                          />
                          <span className="sr-only">Check {cluster.name}</span>
                        </button>
                        <button
                          type="button"
                          onClick={() => remove(cluster)}
                          disabled={removing === cluster.id}
                          className="rounded-sm border border-transparent p-1 text-muted transition-colors hover:border-danger/40 hover:text-danger disabled:opacity-50"
                          title={`Remove ${cluster.name}`}
                        >
                          <Trash2 aria-hidden="true" className="size-3.5" />
                          <span className="sr-only">Remove {cluster.name}</span>
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {loading && clusters.length === 0 ? (
            <p className="px-3 py-6 text-center text-[12px] text-muted">Loading…</p>
          ) : null}

          {!loading && clusters.length === 0 ? (
            <div className="px-3 py-10 text-center">
              <p className="text-[13px] text-fg">No clusters registered</p>
              <p className="mt-1 text-[12px] text-muted">
                Add one to start issuing kubeconfigs for it.
              </p>
              <Link to="/clusters/new" className="mt-3 inline-block">
                <Button variant="secondary">
                  <Plus aria-hidden="true" className="size-3.5" />
                  Add cluster
                </Button>
              </Link>
            </div>
          ) : null}

          {clusters.length > 0 && visible.length === 0 ? (
            <p className="px-3 py-10 text-center text-[12px] text-muted">
              No cluster matches “{filter}”.
            </p>
          ) : null}
        </div>
      </div>

    </AppShell>
  )
}

/**
 * ConnectionTag says how KubeMG reaches a cluster, and for an agent cluster
 * whether its tunnel is up right now — which is different from the last health
 * check, and is the thing an operator actually wants at a glance.
 */
function ConnectionTag({ cluster }: { cluster: Cluster }) {
  if (cluster.connection_mode !== 'agent') {
    return (
      <span className="inline-flex items-center gap-1.5 text-[12px] text-muted" title="Direct API access">
        <Server aria-hidden="true" className="size-3.5 shrink-0" />
        direct
      </span>
    )
  }

  return (
    <span
      className={`inline-flex items-center gap-1.5 text-[12px] ${
        cluster.agent_attached ? 'text-ok' : 'text-muted'
      }`}
      title={cluster.agent_attached ? 'Agent tunnel is open' : 'No agent tunnel'}
    >
      <Plug aria-hidden="true" className="size-3.5 shrink-0" />
      agent
    </span>
  )
}
