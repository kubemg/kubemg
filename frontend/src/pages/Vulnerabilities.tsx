import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Bug, Globe, RefreshCw, ShieldAlert } from 'lucide-react'
import { errorMessage, fetchVulnFindings } from '../api/client'
import type { VulnWorkloadSummary } from '../api/types'
import { AppShell } from '../components/AppShell'
import { ResourceDetailDrawer } from '../components/ResourceDetailDrawer'
import type { DetailTarget } from '../components/ResourceDetailDrawer'
import {
  Age,
  Button,
  EmptyState,
  Notice,
  OBJECT_NAME,
  Pill,
  Row,
  Sheet,
  SearchInput,
  Segmented,
  Table,
  Td,
  Th,
} from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import { VulnIntelSheet } from '../components/VulnIntelSheet'
import { useAuth } from '../state/auth-context'
import { queryKey, useCachedQuery } from '../lib/query'
import { useUrlText } from '../lib/urlState'
import type { VulnLevelFilter } from '../lib/vulnFindings'
import {
  filterVulnRows,
  isKubernetesRow,
  mayAskForVulns,
  vulnBadgeText,
  vulnBadgeTitle,
  vulnResourceKey,
  vulnTone,
} from '../lib/vulnFindings'
import { VulnerabilityPanel } from '../components/VulnerabilityPanel'
import { useClusters } from '../state/clusters-context'

/**
 * What the cluster's own scanner found, one row per workload.
 *
 * KubeMG scanned nothing: Trivy Operator did, in the cluster, and KubeMG reads
 * its reports behind a lease and summarises them. The page is shown only to a
 * KubeMG administrator or a cluster-admin grant with no namespace scope — the
 * server answers 404 to anyone else — because a list of what is vulnerable is
 * an attacker's target list.
 *
 * It opens on what needs attention, not on everything: a real cluster carries
 * hundreds of base-image findings with no fix, and a page that leads with them
 * buries the two that matter. Rust is a known-exploited vulnerability (CISA's
 * KEV), or a fixable critical on something reachable from outside the cluster;
 * amber a fixable critical or high; the rest is a number, one toggle away.
 * Accepted findings are out of every count. Kubernetes' own components (the
 * scanner's cluster report) are one cluster-wide row, opened in a sheet.
 */
export function Vulnerabilities() {
  const params = useParams()
  const clusterId = Number(params.id)
  const { clusters, loading: clustersLoading } = useClusters()
  const cluster = clusters.find((entry) => entry.id === clusterId) ?? null
  const ask = cluster !== null && mayAskForVulns(cluster)

  const state = useCachedQuery(ask ? queryKey('vuln-findings', clusterId) : null, () =>
    fetchVulnFindings(clusterId),
  )
  const [filterParam, setFilterParam] = useUrlText('show', 'attention')
  const filter: VulnLevelFilter = filterParam === 'all' ? 'all' : 'attention'
  const [search, setSearch] = useUrlText('q')
  const [detail, setDetail] = useState<DetailTarget | null>(null)
  const [intelOpen, setIntelOpen] = useState(false)
  const [components, setComponents] = useState<string | null>(null)
  const { user } = useAuth()
  const admin = user?.role === 'admin'

  const data = state.data
  const rows = useMemo(() => filterVulnRows(data?.workloads ?? [], filter, search), [data, filter, search])

  if (!clustersLoading && (!cluster || !ask)) {
    return (
      <AppShell title="Vulnerabilities">
        <div className="card">
          <EmptyState detail="visible" icon={<Bug aria-hidden="true" className="size-5" />} title="Nothing to show here">
            Vulnerability findings are shown to administrators and to people granted cluster-admin on
            the whole cluster.
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const totals = data?.totals
  const loading = state.loading || state.revalidating

  return (
    <AppShell
      title="Vulnerabilities"
      fullWidth
      actions={
        <>
          {admin ? (
            <Button onClick={() => setIntelOpen(true)}>
              <ShieldAlert aria-hidden="true" className="size-4" />
              Exploit intelligence
            </Button>
          ) : null}
          <Button onClick={() => void state.refresh()} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {state.error ? (
          <Notice tone="error">{errorMessage(state.error, 'Could not read this cluster’s findings.')}</Notice>
        ) : null}

        {data && !data.connected ? (
          <div className="card">
            <EmptyState detail="visible" icon={<Bug aria-hidden="true" className="size-5" />} title="Vulnerability alerts are not connected">
              KubeMG reads what a scanner already running in the cluster found; it scans nothing itself.
              An administrator connects one from the{' '}
              <Link to={`/clusters/${clusterId}/dashboard`} className="text-accent hover:underline">
                cluster dashboard
              </Link>
              ’s Integrations panel.
            </EmptyState>
          </div>
        ) : null}

        {data?.connected && !data.enabled ? (
          <Notice tone="info">
            Vulnerability alerts are switched off for this cluster. An administrator turns them back on
            from the cluster dashboard; nothing is read until then.
          </Notice>
        ) : null}

        {data?.connected && data.enabled ? (
          <>
            {data.summary_message ? (
              <Notice tone={data.summary_message.startsWith('The last read failed') ? 'warn' : 'info'}>
                {data.summary_message}{' '}
                {data.summarized_at ? (
                  <>
                    Last read <Age iso={data.summarized_at} />.
                  </>
                ) : (
                  'Not read yet - the first summary is taken when alerts are connected, then every ten minutes.'
                )}
              </Notice>
            ) : null}

            {data.kev_known === false ? (
              <Notice tone="info">
                No known-exploited list is in hand, so nothing here is marked as being exploited in the wild - which is
                not the same as nothing being exploited.{' '}
                {admin ? (
                  <button type="button" className="text-accent hover:underline" onClick={() => setIntelOpen(true)}>
                    Turn on CISA KEV
                  </button>
                ) : (
                  'An administrator turns it on.'
                )}
              </Notice>
            ) : null}

            {totals ? (
              <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
                <Figure label="Needs attention now" value={totals.danger} tone={totals.danger > 0 ? 'bad' : null} />
                <Figure label="Fix waiting" value={totals.warn} tone={totals.warn > 0 ? 'warn' : null} />
                {data.kev_known ? (
                  <Figure label="Known exploited" value={totals.kev} tone={totals.kev > 0 ? 'bad' : null} />
                ) : (
                  <Figure label="Reachable from outside" value={totals.exposed} tone={null} />
                )}
                <Figure label="Workloads scanned" value={totals.workloads} tone={null} />
              </div>
            ) : null}

            <div className="card min-w-0 overflow-hidden">
              <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-5 pt-4 pb-3.5">
                <h2 className="text-[16px] font-bold text-fg">
                  {rows.length} {rows.length === 1 ? 'workload' : 'workloads'}
                </h2>
                <div className="ml-auto flex flex-wrap items-center gap-2">
                  <SearchInput value={search} onChange={setSearch} label="Filter workloads" placeholder="Name, namespace or image…" />
                  <Segmented<VulnLevelFilter>
                    ariaLabel="Which workloads"
                    value={filter}
                    onChange={(next) => setFilterParam(next)}
                    options={[
                      { value: 'attention', label: 'Needs attention' },
                      { value: 'all', label: 'All scanned' },
                    ]}
                  />
                </div>
              </div>
              {state.loading && !data ? (
                <TableSkeleton rows={6} />
              ) : rows.length === 0 ? (
                <p className="px-5 py-6 text-[13px] text-muted">
                  {filter === 'attention'
                    ? 'No workload has a known-exploited, or a fixable critical or high, finding.'
                    : 'Nothing matches.'}
                </p>
              ) : (
                <FindingsTable rows={rows} onOpen={setDetail} onComponents={setComponents} />
              )}
            </div>
          </>
        ) : null}
      </div>
      {detail && cluster ? (
        <ResourceDetailDrawer
          key={`${detail.kind}/${detail.namespace ?? ''}/${detail.name}`}
          cluster={cluster}
          target={detail}
          onClose={() => setDetail(null)}
          onOpen={setDetail}
        />
      ) : null}
      {components && cluster ? (
        <Sheet title="Kubernetes components" eyebrow="Vulnerabilities" width="xl" onClose={() => setComponents(null)}>
          <VulnerabilityPanel cluster={cluster} kind="kubernetes" name={components} namespace="" title="Kubernetes components" />
        </Sheet>
      ) : null}
      {intelOpen ? (
        <VulnIntelSheet
          onClose={() => {
            setIntelOpen(false)
            void state.refresh()
          }}
        />
      ) : null}
    </AppShell>
  )
}

function Figure({ label, value, tone }: { label: string; value: number; tone: 'bad' | 'warn' | null }) {
  const colour = tone === 'bad' ? 'text-danger' : tone === 'warn' ? 'text-warn' : 'text-fg'
  return (
    <div className="card px-4 py-3">
      <p className="text-[12px] text-muted">{label}</p>
      <p className={`font-data text-[22px] font-bold tabular-nums ${colour}`}>{value}</p>
    </div>
  )
}

function FindingsTable({
  rows,
  onOpen,
  onComponents,
}: {
  rows: VulnWorkloadSummary[]
  onOpen: (target: DetailTarget) => void
  onComponents: (name: string) => void
}) {
  return (
    <Table>
      <thead>
        <tr>
          <Th>Workload</Th>
          <Th className="w-[14%]">Namespace</Th>
          <Th className="w-[18%]">Level</Th>
          <Th className="w-[8%]" align="right">Critical</Th>
          <Th className="w-[8%]" align="right">High</Th>
          <Th className="hidden lg:table-cell lg:w-[24%]">Image</Th>
          <Th className="w-[10%]">Scanned</Th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const key = vulnResourceKey(row.kind)
          const tone = vulnTone(row.level)
          const open = key
            ? () => onOpen({ kind: key, label: row.kind, name: row.name, namespace: row.namespace })
            : isKubernetesRow(row)
              ? () => onComponents(row.name)
              : undefined
          return (
            <Row key={`${row.kind}/${row.namespace}/${row.name}`} onOpen={open}>
              <Td>
                {open ? (
                  <button type="button" className={OBJECT_NAME} onClick={open}>
                    {row.name}
                  </button>
                ) : (
                  <span className="font-data font-semibold text-fg">{row.name}</span>
                )}
                <span className="text-[12px] text-muted">{row.kind}</span>
              </Td>
              <Td className="font-data text-[12.5px] text-muted">{row.namespace || 'cluster-wide'}</Td>
              <Td>
                <span className="flex flex-wrap items-center gap-1.5">
                  {tone ? (
                    <Pill tone={tone} title={vulnBadgeTitle(row)}>
                      {vulnBadgeText(row)}
                    </Pill>
                  ) : (
                    <span className="text-[12.5px] text-muted" title={vulnBadgeTitle(row)}>
                      no fix waiting
                    </span>
                  )}
                  {row.exposed ? (
                    <span title="A LoadBalancer or NodePort Service, or a Service an Ingress or HTTPRoute sends to, selects its pods">
                      <Globe aria-label="Reachable from outside the cluster" className="size-3.5 text-muted" />
                    </span>
                  ) : null}
                </span>
              </Td>
              <Td className="text-right font-data text-[12.5px] tabular-nums">{row.critical}</Td>
              <Td className="text-right font-data text-[12.5px] tabular-nums">{row.high}</Td>
              <Td className="hidden font-data text-[12.5px] text-muted lg:table-cell" title={row.images.join(', ')}>
                {row.images[0] ?? '-'}
                {row.images.length > 1 ? ` +${row.images.length - 1}` : ''}
              </Td>
              <Td className="text-[12.5px] text-muted">
                <Age iso={row.scanned_at} />
              </Td>
            </Row>
          )
        })}
      </tbody>
    </Table>
  )
}
