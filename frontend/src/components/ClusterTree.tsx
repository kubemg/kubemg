import { useMemo, useState } from 'react'
import {
  Boxes,
  ChevronDown,
  Cpu,
  Database,
  Gauge,
  KeyRound,
  Network,
  Package,
  Puzzle,
  ScrollText,
  Search,
  Server,
  Shapes,
  ShieldAlert,
  Siren,
  TriangleAlert,
  Waypoints,
} from 'lucide-react'
import { Link, NavLink, useSearchParams } from 'react-router'
import type { Cluster } from '../api/types'
import type { ClusterPage } from '../lib/navigation'
import { clusterPageHref, hasTunnel, pageNeedsTunnel, resourceHref } from '../lib/navigation'
import type {
  CategoryId,
  OperatorCategoryId,
  ResourceCategory,
  ResourceItem,
  ResourceKey,
} from '../lib/resources'
import { isOperatorCategory, matchesResource } from '../lib/resources'

/**
 * ClusterTree is the console's second level of navigation: everything in the
 * open cluster, in one column.
 *
 * It used to be Explore's own sidebar, reached by clicking a row called Explore
 * inside a section called Operate. That put the thing this console exists for
 * three levels down, and made a resource tree compete for the same column as
 * three nav rows pointing back out of it. The tree is now the column itself,
 * drawn on every one of a cluster's pages, and the cluster's own pages are its
 * first group rather than a separate block above it — they are things *in* this
 * cluster, which is how Rancher reads them too.
 *
 * Rows navigate. A resource row is a link to `/clusters/:id/<kind>`, not a
 * callback into a page, because the tree outlives any one page: clicking Pods
 * from the dashboard has to work, and it is the same click either way.
 */

/* One glyph per fixed category, so a category is recognisable before it is read. */
const CATEGORY_ICON: Record<Exclude<CategoryId, OperatorCategoryId>, typeof Boxes> = {
  workloads: Boxes,
  helm: Package,
  networking: Network,
  storage: Database,
  access: KeyRound,
  custom: Puzzle,
  cluster: Server,
  other: Shapes,
}

/**
 * A discovered operator's kinds all share one glyph: which operator it is is
 * already the heading, and inventing a per-vendor icon table would put back the
 * hard-coded list of known operators that discovery exists to avoid.
 */
function categoryIcon(id: CategoryId): typeof Boxes {
  return isOperatorCategory(id) ? Waypoints : CATEGORY_ICON[id]
}

/**
 * Which sections start closed. Two reasons, and both are about the audience:
 * everything built from a cluster's own CRDs can run to dozens of kinds, and
 * RBAC and CRDs are the parts of Kubernetes the people this console is for do
 * not open. The filter still searches inside a closed section, so nothing is
 * hidden, only folded.
 */
function startsCollapsed(id: CategoryId): boolean {
  return id === 'other' || id === 'access' || id === 'custom' || isOperatorCategory(id)
}

/** A cluster's own pages, in the order the work reaches for them. */
const PAGE_ROWS: readonly { page: ClusterPage; label: string; icon: typeof Gauge }[] = [
  { page: 'dashboard', label: 'Dashboard', icon: Gauge },
  { page: 'events', label: 'Events', icon: Siren },
  { page: 'capacity', label: 'Capacity', icon: Cpu },
  { page: 'security', label: 'Security', icon: ShieldAlert },
  { page: 'audit', label: 'Audit trail', icon: ScrollText },
]

type Row =
  | { kind: 'page'; id: string; label: string; page: ClusterPage; icon: typeof Gauge }
  | { kind: 'resource'; id: string; label: string; item: ResourceItem }

interface Group {
  id: string
  label: string
  icon: typeof Boxes
  collapsed: boolean
  rows: Row[]
}

function pageRow(entry: (typeof PAGE_ROWS)[number]): Row {
  return { kind: 'page', id: `page:${entry.page}`, label: entry.label, page: entry.page, icon: entry.icon }
}

function resourceRow(item: ResourceItem): Row {
  return { kind: 'resource', id: `res:${item.key}`, label: item.label, item }
}

/**
 * The tree's groups: the cluster's own group first, then everything the cluster
 * turned out to have.
 *
 * The Cluster group is the five pages with the fixed inventory's own
 * cluster-scoped kinds threaded into it — Dashboard, Nodes, Namespaces, then
 * the three reads and the trail. Nodes and Namespaces are resource lists, but
 * nobody thinks of them as a category of their own next to Workloads: they are
 * how you look at the cluster.
 */
function buildGroups(categories: ResourceCategory[]): Group[] {
  const clusterCategory = categories.find((category) => category.id === 'cluster')
  const rest = categories.filter((category) => category.id !== 'cluster')

  const clusterRows: Row[] = [
    pageRow(PAGE_ROWS[0]),
    ...(clusterCategory?.items ?? []).map(resourceRow),
    ...PAGE_ROWS.slice(1).map(pageRow),
  ]

  return [
    { id: 'cluster', label: 'Cluster', icon: Server, collapsed: false, rows: clusterRows },
    ...rest.map((category) => ({
      id: category.id,
      label: category.label,
      icon: categoryIcon(category.id),
      collapsed: startsCollapsed(category.id),
      rows: category.items.map(resourceRow),
    })),
  ]
}

/** Whether a row can be opened at all, which without a tunnel is most of them. */
function rowIsLive(row: Row, live: boolean): boolean {
  if (live) return true
  return row.kind === 'page' && !pageNeedsTunnel(row.page)
}

const ROW_BASE =
  'relative flex w-full items-center gap-2 rounded-control py-1.5 pr-2 pl-6 text-left text-[13px] transition-colors'

function rowClass(active: boolean): string {
  return active
    ? `${ROW_BASE} bg-rail-raised font-medium text-rail-fg`
    : `${ROW_BASE} text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg`
}

export function ClusterTree({
  cluster,
  categories,
  selected,
}: {
  cluster: Cluster
  categories: ResourceCategory[]
  /** The resource list currently open, or `null` on one of the cluster's pages. */
  selected: ResourceKey | null
}) {
  const [searchParams] = useSearchParams()
  const [filter, setFilter] = useState('')
  // Only what the operator has actually toggled; anything absent falls back to
  // the group's own default. Seeding this with the defaults instead would make
  // the two indistinguishable, and a default could then never change under a
  // session.
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})

  const live = hasTunnel(cluster)
  const needle = filter.trim().toLowerCase()
  // The namespace travels with the kind: someone working in `payments` stays in
  // it while moving from Pods to Services.
  const search = searchParams.toString()

  const groups = useMemo(() => buildGroups(categories), [categories])

  // Filtering hides what does not match and ignores collapse: a search that
  // silently skipped a closed category would be lying about what is here.
  const shown = useMemo(
    () =>
      groups
        .map((group) => ({
          ...group,
          rows: group.rows.filter((row) =>
            row.kind === 'resource'
              ? matchesResource(row.item, needle)
              : row.label.toLowerCase().includes(needle),
          ),
        }))
        .filter((group) => group.rows.length > 0),
    [groups, needle],
  )

  return (
    <div className="flex min-h-0 flex-col">
      <div className="relative mb-2">
        <Search
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-rail-faint"
        />
        <input
          type="search"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Filter resources"
          aria-label="Filter resources"
          className="h-8 w-full rounded-control border border-rail-line bg-rail-raised pr-2 pl-8 text-[13px] text-rail-fg transition-colors placeholder:text-rail-faint hover:border-rail-muted/40 focus:border-accent focus:outline-none"
        />
      </div>

      {/* Without a tunnel the tree keeps its shape and says why most of it is
          unreachable, rather than shedding two thirds of a cluster's
          navigation and leaving the operator to guess what happened. */}
      {live ? null : (
        <p className="mb-2 flex items-start gap-2 rounded-control border border-warn/25 bg-warn-soft px-2.5 py-2 text-[12px] leading-snug text-warn">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
          <span>
            {cluster.connection_mode === 'agent'
              ? 'No agent tunnel right now, so live lists cannot be read. The dashboard and the audit trail still work.'
              : 'This cluster is registered in direct mode, which has no agent tunnel for live reads.'}
          </span>
        </p>
      )}

      <nav aria-label="Cluster" className="min-h-0">
        {shown.map((group) => {
          const holdsSelection = group.rows.some(
            (row) => row.kind === 'resource' && row.item.key === selected,
          )
          const stored = collapsed[group.id]
          const shut = stored ?? (group.collapsed && !holdsSelection)
          // A filter opens everything: a search that skipped a closed section
          // would be lying about what the cluster has.
          const open = needle !== '' || !shut

          return (
            <div key={group.id} className="mb-1">
              <button
                type="button"
                // Toggling from the *effective* state, not from the stored one:
                // negating an absent entry would read as "open" and leave a
                // section that starts closed unable to open on the first click.
                onClick={() => setCollapsed((current) => ({ ...current, [group.id]: !shut }))}
                aria-expanded={open}
                className="flex w-full items-center gap-2 rounded-control px-2 py-1.5 text-left transition-colors hover:bg-rail-raised/60"
              >
                <group.icon aria-hidden="true" className="size-3.5 shrink-0 text-rail-muted" />
                <span className="label min-w-0 flex-1 truncate text-rail-muted">{group.label}</span>
                <ChevronDown
                  aria-hidden="true"
                  className={`size-3.5 shrink-0 text-rail-faint transition-transform ${
                    open ? '' : '-rotate-90'
                  }`}
                />
              </button>

              {open ? (
                <ul className="mt-0.5 flex flex-col gap-px">
                  {group.rows.map((row) => (
                    <li key={row.id}>
                      <TreeRow
                        row={row}
                        cluster={cluster}
                        search={search}
                        selected={selected}
                        live={rowIsLive(row, live)}
                      />
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          )
        })}

        {shown.length === 0 ? (
          <p className="px-2 py-3 text-[12.5px] text-rail-faint">Nothing matches “{filter}”.</p>
        ) : null}
      </nav>
    </div>
  )
}

/** The active marker, drawn inside the row's left padding. */
function Marker() {
  return (
    <span
      aria-hidden="true"
      className="absolute top-1/2 left-2 h-4 w-[3px] -translate-y-1/2 rounded-full bg-accent"
    />
  )
}

function TreeRow({
  row,
  cluster,
  search,
  selected,
  live,
}: {
  row: Row
  cluster: Cluster
  search: string
  selected: ResourceKey | null
  live: boolean
}) {
  // A row that cannot be opened is drawn rather than removed, and says so to a
  // screen reader as well as to the eye. A link that only ever answers "this
  // needs a tunnel" is worse than a row you can see is unavailable.
  if (!live) {
    return (
      <span
        aria-disabled="true"
        title={`${row.label} needs this cluster's tunnel`}
        className={`${ROW_BASE} cursor-not-allowed text-rail-muted opacity-45`}
      >
        <span className="min-w-0 flex-1 truncate">{row.label}</span>
      </span>
    )
  }

  if (row.kind === 'page') {
    return (
      <NavLink
        to={clusterPageHref(cluster.id, row.page)}
        title={row.label}
        className={({ isActive }) => rowClass(isActive)}
      >
        {({ isActive }) => (
          <>
            {isActive ? <Marker /> : null}
            <span className="min-w-0 flex-1 truncate">{row.label}</span>
          </>
        )}
      </NavLink>
    )
  }

  const active = row.item.key === selected
  return (
    <Link
      to={resourceHref(cluster.id, row.item.key, search)}
      aria-current={active ? 'page' : undefined}
      title={row.label}
      className={rowClass(active)}
    >
      {active ? <Marker /> : null}
      <span className="min-w-0 flex-1 truncate">{row.label}</span>
      {/* Cluster-scoped lists ignore the namespace picker; saying so here is
          why it disappears. */}
      {row.item.scope === 'cluster' ? (
        <span className="shrink-0 font-mono text-[10px] text-rail-faint">cluster</span>
      ) : null}
    </Link>
  )
}
