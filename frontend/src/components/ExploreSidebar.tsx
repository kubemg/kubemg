import { useMemo, useState } from 'react'
import { Boxes, ChevronDown, Database, Network, Puzzle, Search, Server } from 'lucide-react'
import type { CategoryId, ResourceKey } from '../lib/resources'
import { RESOURCE_CATEGORIES, matchesResource } from '../lib/resources'

/* One glyph per category, so a category is recognisable before it is read. */
const CATEGORY_ICON: Record<CategoryId, typeof Boxes> = {
  workloads: Boxes,
  networking: Network,
  storage: Database,
  custom: Puzzle,
  cluster: Server,
}

/**
 * ExploreSidebar is the third level of navigation: which kind of object in the
 * cluster you are looking at. Level one picks the part of KubeMG, level two the
 * cluster, and this the resource — the same three moves Rancher and Lens make,
 * because a fleet console is browsed that way.
 *
 * It carries the rail palette rather than the work surface: it is chrome, and it
 * sits flush against the level-two panel so the three levels read as one deck.
 */
export function ExploreSidebar({
  selected,
  onSelect,
}: {
  selected: ResourceKey
  onSelect: (resource: ResourceKey) => void
}) {
  const [filter, setFilter] = useState('')
  const [collapsed, setCollapsed] = useState<Partial<Record<CategoryId, boolean>>>({})

  const needle = filter.trim().toLowerCase()

  // Filtering hides what does not match and ignores collapse: a search that
  // silently skipped a collapsed category would be lying.
  const categories = useMemo(
    () =>
      RESOURCE_CATEGORIES.map((category) => ({
        ...category,
        items: category.items.filter((item) => matchesResource(item, needle)),
      })).filter((category) => category.items.length > 0),
    [needle],
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-14 shrink-0 items-center px-4">
        <p className="label text-rail-faint">Resources</p>
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
          const open = needle !== '' || !collapsed[category.id]

          return (
            <div key={category.id} className="mb-1">
              <button
                type="button"
                onClick={() =>
                  setCollapsed((current) => ({ ...current, [category.id]: !collapsed[category.id] }))
                }
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
