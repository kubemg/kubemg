import { useMemo, useState } from 'react'
import {
  Boxes,
  ChevronDown,
  Database,
  Network,
  Package,
  Puzzle,
  Search,
  Server,
  Shapes,
  Waypoints,
} from 'lucide-react'
import type { Cluster } from '../api/types'
import { EnvironmentDot } from './primitives'
import type { CategoryId, ResourceCategory, ResourceKey } from '../lib/resources'
import { RESOURCE_CATEGORIES, matchesResource } from '../lib/resources'

/**
 * The sections that start closed. Both are built from the cluster's own CRDs and
 * both can run long — a mesh installs a dozen kinds and Other is however many
 * every operator on the cluster brought with it. Open by default they push the
 * fixed inventory off the screen, which costs the resources people actually
 * browse to show ones they rarely do. The filter still searches inside a closed
 * section, so nothing is hidden, only folded.
 */
const START_COLLAPSED: Partial<Record<CategoryId, boolean>> = {
  istio: true,
  other: true,
}

/* One glyph per category, so a category is recognisable before it is read. */
const CATEGORY_ICON: Record<CategoryId, typeof Boxes> = {
  workloads: Boxes,
  helm: Package,
  networking: Network,
  storage: Database,
  custom: Puzzle,
  cluster: Server,
  istio: Waypoints,
  other: Shapes,
}

/**
 * ExploreSidebar is the third level of navigation: which kind of object in the
 * cluster you are looking at. Level one picks the part of KubeMG, level two the
 * cluster, and this the resource — the same three moves Rancher and Lens make,
 * because a fleet console is browsed that way.
 *
 * It carries the rail palette rather than the work surface: it is chrome, and it
 * sits flush against the level-two panel so the three levels read as one deck.
 *
 * `categories` is passed in rather than read from the fixed inventory because
 * part of it is not fixed: a cluster running Istio gets an Istio section, and a
 * cluster's own CRDs are listed under Custom Resources. What is browsable is a
 * property of the cluster, so only the page that knows which cluster is open can
 * decide it.
 */
export function ExploreSidebar({
  categories: inventory = RESOURCE_CATEGORIES,
  cluster,
  selected,
  onSelect,
}: {
  categories?: ResourceCategory[]
  /**
   * Whose resources these are. It is named here because this column is the one
   * place that is certainly on screen while Explore is open: the section panel
   * defaults to icon width on a page with a third level, and there a cluster in
   * the fleet list is a dot with its name on hover.
   */
  cluster?: Cluster
  selected: ResourceKey
  onSelect: (resource: ResourceKey) => void
}) {
  const [filter, setFilter] = useState('')
  // Only what the operator has actually toggled; anything absent falls back to
  // START_COLLAPSED. Seeding this with the defaults instead would make the two
  // indistinguishable, and a default could then never change under a session.
  const [collapsed, setCollapsed] = useState<Partial<Record<CategoryId, boolean>>>({})

  const needle = filter.trim().toLowerCase()

  // Filtering hides what does not match and ignores collapse: a search that
  // silently skipped a collapsed category would be lying.
  const categories = useMemo(
    () =>
      inventory
        .map((category) => ({
          ...category,
          items: category.items.filter((item) => matchesResource(item, needle)),
        }))
        .filter((category) => category.items.length > 0),
    [inventory, needle],
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-14 shrink-0 items-center gap-2 px-4">
        {cluster ? (
          <>
            <EnvironmentDot environment={cluster.environment} />
            <span
              title={cluster.name}
              className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-rail-fg"
            >
              {cluster.name}
            </span>
          </>
        ) : (
          <p className="label text-rail-faint">Resources</p>
        )}
      </div>

      <div className="shrink-0 px-3 pb-3">
        <div className="relative">
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
      </div>

      <nav aria-label="Cluster resources" className="min-h-0 flex-1 overflow-y-auto px-2.5 pb-4">
        {categories.map((category) => {
          const Icon = CATEGORY_ICON[category.id]
          // A section holding the current selection starts open — otherwise
          // picking an Istio resource from the filter and then clearing it
          // would fold away the very thing being shown. An explicit toggle
          // still wins over both defaults.
          const holdsSelection = category.items.some((item) => item.key === selected)
          const shut =
            collapsed[category.id] ?? (START_COLLAPSED[category.id] && !holdsSelection) ?? false
          // A filter opens everything: a search that skipped a closed section
          // would be lying about what the cluster has.
          const open = needle !== '' || !shut

          return (
            <div key={category.id} className="mb-1">
              <button
                type="button"
                // Toggling from the *effective* state, not from the stored one:
                // negating an absent entry would read as "open" and leave a
                // section that starts closed unable to open on the first click.
                onClick={() => setCollapsed((current) => ({ ...current, [category.id]: !shut }))}
                aria-expanded={open}
                className="flex w-full items-center gap-2 rounded-control px-2 py-1.5 text-left transition-colors hover:bg-rail-raised/60"
              >
                <Icon aria-hidden="true" className="size-3.5 shrink-0 text-rail-muted" />
                <span className="label min-w-0 flex-1 truncate text-rail-muted">
                  {category.label}
                </span>
                <ChevronDown
                  aria-hidden="true"
                  className={`size-3.5 shrink-0 text-rail-faint transition-transform ${
                    open ? '' : '-rotate-90'
                  }`}
                />
              </button>

              {open ? (
                <ul className="mt-0.5 flex flex-col gap-px">
                  {category.items.map((item) => {
                    const active = item.key === selected
                    return (
                      <li key={item.key}>
                        <button
                          type="button"
                          onClick={() => onSelect(item.key)}
                          aria-current={active ? 'true' : undefined}
                          title={item.label}
                          className={`relative flex w-full items-center gap-2 rounded-control py-1.5 pr-2 pl-6 text-left text-[13px] transition-colors ${
                            active
                              ? 'bg-rail-raised font-medium text-rail-fg'
                              : 'text-rail-muted hover:bg-rail-raised/60 hover:text-rail-fg'
                          }`}
                        >
                          {active ? (
                            <span
                              aria-hidden="true"
                              className="absolute top-1/2 left-2 h-4 w-[3px] -translate-y-1/2 rounded-full bg-accent"
                            />
                          ) : null}
                          <span className="min-w-0 flex-1 truncate">{item.label}</span>
                          {/* Cluster-scoped lists ignore the namespace picker;
                              saying so here is why it disappears. */}
                          {item.scope === 'cluster' ? (
                            <span className="shrink-0 font-mono text-[10px] text-rail-faint">
                              cluster
                            </span>
                          ) : null}
                        </button>
                      </li>
                    )
                  })}
                </ul>
              ) : null}
            </div>
          )
        })}

        {categories.length === 0 ? (
          <p className="px-2 py-3 text-[12.5px] text-rail-faint">
            Nothing matches “{filter}”.
          </p>
        ) : null}
      </nav>

    </div>
  )
}
