import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Plug, Plus, RefreshCw, Server, Trash2 } from 'lucide-react'
import { checkCluster, deleteCluster, errorMessage } from '../api/client'
import type { Cluster } from '../api/types'
import { AppShell } from '../components/AppShell'
import { LinkStrand } from '../components/LinkStrand'
import {
  Button,
  ClusterState,
  EmptyState,
  EnvironmentTag,
  IconButton,
  Notice,
  Row,
  SearchInput,
  Table,
  Td,
  Th,
} from '../components/primitives'
import { strandState } from '../lib/status'
import { relativeAge } from '../lib/time'
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

  const needle = filter.trim().toLowerCase()
  const visible = needle
    ? clusters.filter((cluster) => cluster.name.toLowerCase().includes(needle))
    : clusters

  return (
    <AppShell
      title="Clusters"
      actions={
        <Link to="/clusters/new">
          <Button variant="primary">
            <Plus aria-hidden="true" className="size-4" />
            Register cluster
          </Button>
        </Link>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {listError ? <Notice tone="error">{listError}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <SearchInput
              value={filter}
              onChange={setFilter}
              label="Filter clusters by name"
              placeholder="Filter by name"
            />
            <span className="ml-auto text-[13px] text-muted">
              {visible.length === clusters.length
                ? `${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'}`
                : `${visible.length} of ${clusters.length}`}
            </span>
          </div>

          {/* Narrow screens drop the two widest, least scannable columns rather
              than forcing the whole page to scroll sideways. */}
          <Table>
            <thead>
              <tr>
                <Th className="w-[38%] md:w-[22%]">Cluster</Th>
                <Th className="w-[26%] md:w-[10%]">Environment</Th>
                <Th className="hidden md:table-cell md:w-[16%]">Link</Th>
                <Th className="hidden md:table-cell md:w-[22%]">API server</Th>
                <Th className="w-[24%] md:w-[13%]">State</Th>
                <Th className="hidden md:table-cell md:w-[9%]">Version</Th>
                <Th align="right" className="w-[12%] md:w-[8%]">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {visible.map((cluster) => (
                <Row key={cluster.id}>
                  <Td className="truncate">
                    <Link
                      to={`/clusters/${cluster.id}`}
                      className="font-mono text-fg transition-colors hover:text-accent"
                    >
                      {cluster.name}
                    </Link>
                  </Td>
                  <Td>
                    <EnvironmentTag environment={cluster.environment} />
                  </Td>
                  <Td className="hidden md:table-cell">
                    <span className="flex items-center gap-2">
                      {cluster.connection_mode === 'agent' ? (
                        <Plug aria-hidden="true" className="size-3.5 shrink-0 text-faint" />
                      ) : (
                        <Server aria-hidden="true" className="size-3.5 shrink-0 text-faint" />
                      )}
                      <LinkStrand state={strandState(cluster)} className="w-14 shrink-0" />
                      <span className="font-mono text-[12px] text-muted">
                        {cluster.connection_mode === 'agent'
                          ? cluster.agent_attached
                            ? 'up'
                            : 'down'
                          : 'direct'}
                      </span>
                    </span>
                  </Td>
                  <Td
                    className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell"
                    title={cluster.api_url}
                  >
                    {/* An agent cluster has no API URL here on purpose: KubeMG
                        never learns one, it just answers the tunnel. */}
                    {cluster.api_url || 'via agent tunnel'}
                  </Td>
                  <Td title={cluster.status_message}>
                    <span className="flex flex-col items-start gap-0.5">
                      <ClusterState cluster={cluster} />
                      <span className="text-[11.5px] text-faint">
                        {cluster.status === 'pending' ? '' : relativeAge(cluster.last_checked_at)}
                      </span>
                    </span>
                  </Td>
                  <Td className="hidden truncate font-mono text-[12.5px] text-muted md:table-cell">
                    {cluster.kubernetes_version ?? '—'}
                  </Td>
                  <Td>
                    <div className="flex items-center justify-end gap-0.5">
                      <IconButton
                        label={`Check ${cluster.name}`}
                        onClick={() => check(cluster)}
                        disabled={checking === cluster.id}
                      >
                        <RefreshCw
                          aria-hidden="true"
                          className={`size-3.5 ${checking === cluster.id ? 'animate-spin' : ''}`}
                        />
                      </IconButton>
                      <IconButton
                        label={`Remove ${cluster.name}`}
                        tone="danger"
                        onClick={() => remove(cluster)}
                        disabled={removing === cluster.id}
                      >
                        <Trash2 aria-hidden="true" className="size-3.5" />
                      </IconButton>
                    </div>
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>

          {loading && clusters.length === 0 ? (
            <p className="px-4 py-8 text-center text-[13px] text-muted">Loading…</p>
          ) : null}

          {!loading && clusters.length === 0 ? (
            <EmptyState
              icon={<Server aria-hidden="true" className="size-5" />}
              title="No clusters registered"
              action={
                <Link to="/clusters/new">
                  <Button variant="primary">
                    <Plus aria-hidden="true" className="size-4" />
                    Register cluster
                  </Button>
                </Link>
              }
            >
              Registration walks through identity, how KubeMG reaches the cluster, the handshake,
              and who gets access.
            </EmptyState>
          ) : null}

          {clusters.length > 0 && visible.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              No cluster matches “{filter}”.
            </p>
          ) : null}
        </div>
      </div>
    </AppShell>
  )
}
