import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, Layers, Search } from 'lucide-react'
import type { Cluster, Environment } from '../api/types'
import { linkState } from '../lib/status'
import { LinkStatus } from './LinkStatus'
import { EnvironmentTag } from './primitives'

/**
 * Which environment comes first. It is the order of consequence, not the
 * alphabet: the clusters where a mistake costs the most are the ones that must
 * be hardest to pick by accident and easiest to recognise once picked.
 */
const ENVIRONMENTS: ReadonlyArray<{ id: Environment; label: string }> = [
  { id: 'prod', label: 'Production' },
  { id: 'staging', label: 'Staging' },
  { id: 'dev', label: 'Development' },
]

/**
 * ClusterMenu is *the* list of clusters as a chooser — one component behind
 * both switchers, the header's and the section panel's, so a fleet cannot look
 * like two different fleets depending on which one you opened.
 *
 * It groups by environment rather than listing a flat run of names with a dot
 * beside each. A dot is a colour and nothing else, and the single most
 * consequential fact about a cluster you are about to open a shell in is
 * whether it is production — so the environment is a heading with a word in it
 * and the rows sit underneath, which also means the fleet reads as three short
 * lists rather than one long one.
 *
 * The filter is here because a chooser that needs scrolling is a chooser that
 * needs typing; ⌘K remains the way to reach a page as well as a cluster.
 */
export function ClusterMenu({
  clusters,
  currentId,
  onPick,
  onFleet,
  onClose,
  className,
}: {
  clusters: Cluster[]
  /** The cluster the caller is already in, if any — marked, never hidden. */
  currentId?: number
  onPick: (cluster: Cluster) => void
  /** Leaving every cluster for the fleet, the last row of the list. */
  onFleet: () => void
  onClose: () => void
  className?: string
}) {
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const listRef = useRef<HTMLDivElement | null>(null)

  const needle = query.trim().toLowerCase()

  // One flat run in the order the groups draw them, so the arrow keys walk the
  // list the eye sees rather than the array the fleet arrived in.
  const ordered = useMemo(() => {
    const matching = clusters.filter(
      (cluster) => !needle || cluster.name.toLowerCase().includes(needle),
    )
    return ENVIRONMENTS.flatMap(({ id }) =>
      matching.filter((cluster) => cluster.environment === id),
    )
  }, [clusters, needle])

  // The trailing fleet row shares the roving order, one past the last cluster.
  const rowCount = ordered.length + 1

  useEffect(() => {
    const openIndex = ordered.findIndex((entry) => entry.id === currentId)
    setCursor(openIndex === -1 ? 0 : openIndex)
    // Re-homing the cursor is about the list changing under it, not about the
    // caller re-rendering, so this deliberately keys off the filtered run.
  }, [ordered, currentId])

  useEffect(() => {
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [cursor])

  function onKeyDown(event: React.KeyboardEvent) {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onClose()
      return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setCursor((current) => (current + 1) % rowCount)
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setCursor((current) => (current - 1 + rowCount) % rowCount)
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      if (cursor === ordered.length) onFleet()
      else if (ordered[cursor]) onPick(ordered[cursor])
    }
  }

  return (
    <div
      role="menu"
      aria-label="Switch cluster"
      onKeyDown={onKeyDown}
      className={`pop-in card flex max-h-[70vh] w-76 flex-col overflow-hidden lift ${className ?? ''}`}
    >
      <div className="flex shrink-0 items-center gap-2.5 border-b border-line-soft px-3">
        <Search aria-hidden="true" className="size-3.5 shrink-0 text-faint" />
        <input
          autoFocus
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Filter clusters"
          aria-label="Filter clusters"
          className="h-10 min-w-0 flex-1 bg-transparent text-[13px] text-fg placeholder:text-faint focus:outline-none"
        />
      </div>

      <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto p-1.5">
        {ENVIRONMENTS.map(({ id, label }) => {
          const rows = ordered.filter((cluster) => cluster.environment === id)
          if (rows.length === 0) return null
          return (
            <div key={id} className="mb-1 last:mb-0">
              <p className="flex items-center gap-2 px-2 pt-2 pb-1.5">
                <EnvironmentTag environment={id} />
                <span className="label text-faint">{label}</span>
                <span className="ml-auto font-mono text-[11px] text-faint">{rows.length}</span>
              </p>
              <ul className="flex flex-col gap-0.5">
                {rows.map((entry) => {
                  const index = ordered.indexOf(entry)
                  const active = entry.id === currentId
                  return (
                    <li key={entry.id}>
                      <button
                        type="button"
                        role="menuitem"
                        aria-current={active ? 'true' : undefined}
                        data-active={index === cursor}
                        onMouseEnter={() => setCursor(index)}
                        onClick={() => onPick(entry)}
                        className={`flex w-full items-center gap-2.5 rounded-control px-2.5 py-1.5 text-left transition-colors ${
                          index === cursor ? 'bg-accent-soft' : 'hover:bg-raised'
                        }`}
                      >
                        <span
                          className={`min-w-0 flex-1 truncate font-mono text-[13px] ${
                            active ? 'text-accent' : 'text-fg'
                          }`}
                        >
                          {entry.name}
                        </span>
                        {entry.kubernetes_version ? (
                          <span className="shrink-0 font-mono text-[11px] text-faint">
                            {entry.kubernetes_version}
                          </span>
                        ) : null}
                        <LinkStatus state={linkState(entry)} variant="glyph" />
                        {active ? (
                          <Check aria-hidden="true" className="size-3.5 shrink-0 text-accent" />
                        ) : (
                          <span aria-hidden="true" className="size-3.5 shrink-0" />
                        )}
                      </button>
                    </li>
                  )
                })}
              </ul>
            </div>
          )
        })}

        {ordered.length === 0 ? (
          <p className="px-3 py-6 text-center text-[12.5px] text-muted">
            No cluster matches “{query}”.
          </p>
        ) : null}
      </div>

      {/* Leaving a cluster is as ordinary as moving between two, so it is the
          last row of the same list rather than a separate way out. */}
      <div className="shrink-0 border-t border-line-soft p-1.5">
        <button
          type="button"
          role="menuitem"
          data-active={cursor === ordered.length}
          onMouseEnter={() => setCursor(ordered.length)}
          onClick={onFleet}
          className={`flex w-full items-center gap-2.5 rounded-control px-2.5 py-2 text-left text-[13px] transition-colors ${
            cursor === ordered.length
              ? 'bg-raised text-fg'
              : 'text-muted hover:bg-raised hover:text-fg'
          }`}
        >
          <Layers aria-hidden="true" className="size-3.5 shrink-0" />
          Fleet overview
        </button>
      </div>
    </div>
  )
}
