import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { CircleDashed, Pencil, Plug, Plus, RefreshCw, Server, Trash2, Unplug, Waypoints, X } from 'lucide-react'
import { checkCluster, deleteCluster, errorMessage } from '../api/client'
import type { Cluster } from '../api/types'
import { AppShell } from '../components/AppShell'
import { ClusterLabelsSheet } from '../components/ClusterLabelsSheet'
import { LinkStatus } from '../components/LinkStatus'
import {
  Age,
  Chip,
  ClusterState,
  EmptyState,
  EnvironmentTag,
  IconButton,
  LinkButton,
  Notice,
  OBJECT_NAME,
  Row,
  SearchInput,
  StatTile,
  Table,
  Td,
  Th,
} from '../components/primitives'
import { railChip } from '../lib/branding'
import { fleetLinkCounts, newestAgentVersion } from '../lib/fleet'
import { AGENT_BEHIND, AGENT_FILTER_PARAM, agentsBehind } from '../lib/fleetStrip'
import { linkState } from '../lib/status'
import { useClusters } from '../state/clusters-context'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'

export function ClusterManagement() {
  const confirm = useConfirm()
  const report = useResult()
  const { clusters, loading, error: listError, reload } = useClusters()
  const [rowError, setRowError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)
  const [filter, setFilter] = useState('')
  const [checking, setChecking] = useState<number | null>(null)
  const [editing, setEditing] = useState<Cluster | null>(null)
  // `?agent=behind` is how the fleet's drift figure opens this page on the
  // clusters it counted — the same derivation, so the count and the rows agree.
  const [searchParams, setSearchParams] = useSearchParams()
  const behindOnly = searchParams.get(AGENT_FILTER_PARAM) === AGENT_BEHIND
  const behind = useMemo(() => new Set(agentsBehind(clusters).map((c) => c.id)), [clusters])
  const newestAgent = useMemo(() => newestAgentVersion(clusters), [clusters])

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
    const confirmed = await confirm({
      eyebrow: 'Cluster',
      title: `Remove ${cluster.name}?`,
      body: 'Its grants, its datasources and its console links go with it. Kubeconfigs already issued keep working until they expire — revoke them from Issued credentials if that is not what you want.',
      confirmLabel: 'Remove',
    })
    if (!confirmed) return

    setRemoving(cluster.id)
    setRowError(null)
    try {
      await deleteCluster(cluster.id)
      await reload()
      report({
        tone: 'ok',
        title: `Removed ${cluster.name}`,
        body: 'Its grants and datasources went with it. Kubeconfigs already issued run until they expire.',
        link: { to: '/audit', label: 'See it in the audit trail' },
      })
    } catch (err) {
      // The row error is what somebody still looking at this table reads; the
      // strip is what reaches them once the act has moved them elsewhere.
      const message = errorMessage(err, `Could not remove ${cluster.name}.`)
      setRowError(message)
      report({ tone: 'error', title: `${cluster.name} was not removed`, body: message })
    } finally {
      setRemoving(null)
    }
  }

  const needle = filter.trim().toLowerCase()
  const visible = clusters.filter(
    (cluster) =>
      (!needle || cluster.name.toLowerCase().includes(needle)) &&
      (!behindOnly || behind.has(cluster.id)),
  )

  function showBehindOnly() {
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        next.set(AGENT_FILTER_PARAM, AGENT_BEHIND)
        return next
      },
      { replace: true },
    )
  }

  const links = fleetLinkCounts(clusters)

  function showEveryAgent() {
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        next.delete(AGENT_FILTER_PARAM)
        return next
      },
      { replace: true },
    )
  }

  return (
    <AppShell
      title="Clusters"
      description="Every cluster kubemg is registered to reach, how it reaches it, and whether that link is up right now."
      actions={
        <LinkButton to="/admin/clusters/new" variant="primary">
            <Plus aria-hidden="true" className="size-4" />
            Register cluster
        </LinkButton>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {listError ? <Notice tone="error">{listError}</Notice> : null}
        {rowError ? <Notice tone="error">{rowError}</Notice> : null}

        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <StatTile icon={Server} label="Registered" value={clusters.length} />
          <StatTile
            icon={Waypoints}
            label="Linked"
            value={links.live + links.direct}
            sub={`${links.live} tunnel · ${links.direct} direct`}
            tone={links.live + links.direct > 0 ? 'ok' : 'neutral'}
          />
          <StatTile
            icon={links.down > 0 ? Unplug : CircleDashed}
            label={links.down > 0 ? 'Down or waiting' : 'Waiting to dial in'}
            value={links.down + links.idle}
            sub={links.down > 0 ? `${links.down} down` : undefined}
            tone={links.down > 0 ? 'danger' : links.idle > 0 ? 'warn' : 'neutral'}
          />
          {/* The one figure here that filters the list under it: an agent
              behind is the row an operator goes looking for. */}
          <StatTile
            icon={Plug}
            label="Agents behind"
            value={behind.size}
            sub={newestAgent ? `newest ${newestAgent}` : undefined}
            tone={behind.size > 0 ? 'warn' : 'neutral'}
            onClick={behind.size > 0 || behindOnly ? (behindOnly ? showEveryAgent : showBehindOnly) : undefined}
            pressed={behindOnly}
          />
        </div>

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-5 pt-4 pb-3.5">
            <SearchInput
              value={filter}
              onChange={setFilter}
              label="Filter clusters by name"
              placeholder="Filter by name…"
            />
            {behindOnly ? (
              <Chip active title="Show every cluster again" onClick={showEveryAgent}>
                Agent behind {newestAgent ?? 'the fleet'}
                <X aria-hidden="true" className="size-3.5" />
              </Chip>
            ) : null}
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
                <Th className="w-[38%] md:w-[20%]">Cluster</Th>
                {/* The chip has a column because the whole reason it is chosen
                    rather than derived is that two of them must not collide —
                    and a collision is only visible when they are in a line. */}
                <Th className="hidden md:table-cell md:w-[6%]">Chip</Th>
                <Th className="w-[26%] md:w-[9%]">Environment</Th>
                <Th className="hidden md:table-cell md:w-[14%]">Link</Th>
                <Th className="hidden md:table-cell md:w-[18%]">API server</Th>
                <Th className="w-[24%] md:w-[12%]">State</Th>
                <Th className="hidden md:table-cell md:w-[9%]">Version</Th>
                <Th align="right" className="w-[12%] md:w-[12%]">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {visible.map((cluster) => (
                <Row key={cluster.id}>
                  <Td>
                    <span className="flex">
                      <Link to={`/clusters/${cluster.id}`} className={OBJECT_NAME}>
                        {cluster.name}
                      </Link>
                    </span>
                  </Td>
                  <Td className="hidden md:table-cell">
                    <span className="inline-grid h-8 min-w-8 place-items-center rounded-control border border-line-soft bg-raised px-1.5 font-data text-[11px] font-semibold text-fg">
                      {railChip(cluster)}
                    </span>
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
                      <LinkStatus state={linkState(cluster)} />
                    </span>
                  </Td>
                  <Td
                    className="hidden truncate font-data text-[12.5px] text-muted md:table-cell"
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
                        {cluster.status === 'pending' ? '' : <Age iso={cluster.last_checked_at} />}
                      </span>
                    </span>
                  </Td>
                  <Td className="hidden truncate font-data text-[12.5px] text-muted md:table-cell">
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
                        label={`Edit ${cluster.name}`}
                        onClick={() => setEditing(cluster)}
                      >
                        <Pencil aria-hidden="true" className="size-3.5" />
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
                <LinkButton to="/admin/clusters/new" variant="primary">
                    <Plus aria-hidden="true" className="size-4" />
                    Register cluster
                </LinkButton>
              }
            >
              Registration walks through identity, how kubemg reaches the cluster, the handshake,
              and who gets access.
            </EmptyState>
          ) : null}

          {clusters.length > 0 && visible.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              {behindOnly && !needle
                ? 'Every agent is on the newest version running in the fleet.'
                : `No cluster matches “${filter}”.`}
            </p>
          ) : null}
        </div>
      </div>

      {editing ? (
        <ClusterLabelsSheet
          cluster={editing}
          onClose={() => setEditing(null)}
          onSaved={reload}
        />
      ) : null}
    </AppShell>
  )
}
