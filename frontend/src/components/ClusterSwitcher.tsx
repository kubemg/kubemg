import { useEffect, useRef, useState } from 'react'
import { ChevronDown, Layers } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router'
import type { Cluster } from '../api/types'
import { strandState } from '../lib/status'
import { useClusters } from '../state/clusters-context'
import { LinkStrand } from './LinkStrand'
import { EnvironmentDot, EnvironmentTag } from './primitives'

type ClusterView = 'summary' | 'explore' | 'audit'

/** Which of a cluster's own views the address currently names, Summary when it
    does not — the same view `clusterHref` opens by default. */
function currentView(pathname: string, clusterId: number): ClusterView {
  const match = pathname.match(
    new RegExp(`^/clusters/${clusterId}/(summary|explore|audit)(?:/|$)`),
  )
  return (match?.[1] as ClusterView | undefined) ?? 'summary'
}

/** Every cluster has a summary and a trail; only a live tunnel has resources
    to explore, so that is the one view a target can lack. */
function hasView(cluster: Cluster, view: ClusterView): boolean {
  if (view !== 'explore') return true
  return cluster.connection_mode === 'agent' && cluster.agent_attached
}

/**
 * ClusterSwitcher stands in for the header's plain title once a cluster is
 * open: a cluster is a place, not a page, and the fastest way out of one is
 * into another rather than back through the fleet list first. Picking one
 * keeps whichever view is open — Summary stays Summary, Explore stays Explore
 * — and falls back to Summary on a target that does not have it.
 *
 * The interaction shape is `CommandPalette`'s: a roving cursor driven by the
 * arrow keys, Enter to commit, Escape and an outside click to back out,
 * rather than a second pattern for one more menu.
 */
export function ClusterSwitcher({ cluster }: { cluster: Cluster }) {
  const { clusters } = useClusters()
  const { pathname } = useLocation()
  const navigate = useNavigate()

  const [open, setOpen] = useState(false)
  const [cursor, setCursor] = useState(0)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const listRef = useRef<HTMLUListElement | null>(null)

  const view = currentView(pathname, cluster.id)
  // The trailing "whole fleet" row shares the roving order, one past the last cluster.
  const rowCount = clusters.length + 1

  useEffect(() => {
    if (!open) return
    const openIndex = clusters.findIndex((entry) => entry.id === cluster.id)
    setCursor(openIndex === -1 ? 0 : openIndex)

    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') setOpen(false)
    }
    function onOutside(event: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    window.addEventListener('mousedown', onOutside)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('mousedown', onOutside)
    }
  }, [open, clusters, cluster.id])

  useEffect(() => {
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [cursor])

  function goTo(target: Cluster) {
    setOpen(false)
    navigate(`/clusters/${target.id}/${hasView(target, view) ? view : 'summary'}`)
  }

  function backToFleet() {
    setOpen(false)
    navigate('/')
  }

  function onMenuKeyDown(event: React.KeyboardEvent) {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setCursor((current) => (current + 1) % rowCount)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setCursor((current) => (current - 1 + rowCount) % rowCount)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      if (cursor === clusters.length) {
        backToFleet()
      } else if (clusters[cursor]) {
        goTo(clusters[cursor])
      }
    }
  }

  return (
    <div ref={rootRef} className="relative min-w-0">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault()
            setOpen(true)
          }
        }}
        className="flex min-w-0 items-center gap-1.5 rounded-control py-1 pr-1.5 pl-1 text-[15px] font-semibold text-fg transition-colors hover:bg-raised"
      >
        <EnvironmentDot environment={cluster.environment} />
        <span className="min-w-0 truncate font-mono">{cluster.name}</span>
        <ChevronDown
          aria-hidden="true"
          className={`size-3.5 shrink-0 text-faint transition-transform ${open ? 'rotate-180' : ''}`}
        />
      </button>

      {open ? (
        <div
          role="menu"
          aria-label="Switch cluster"
          onKeyDown={onMenuKeyDown}
          className="pop-in card absolute top-full left-0 z-20 mt-1.5 max-h-[70vh] w-72 overflow-y-auto p-1.5 lift"
        >
          <ul ref={listRef} className="flex flex-col gap-0.5">
            {clusters.map((entry, index) => {
              const active = entry.id === cluster.id
              return (
                <li key={entry.id}>
                  <button
                    type="button"
                    role="menuitem"
                    aria-current={active ? 'true' : undefined}
                    data-active={index === cursor}
                    onMouseEnter={() => setCursor(index)}
                    onClick={() => goTo(entry)}
                    className={`flex w-full items-center gap-2.5 rounded-control px-2.5 py-2 text-left transition-colors ${
                      index === cursor ? 'bg-accent-soft' : 'hover:bg-raised'
                    }`}
                  >
                    <EnvironmentDot environment={entry.environment} />
                    <span
                      className={`min-w-0 flex-1 truncate font-mono text-[13px] ${
                        active ? 'text-accent' : 'text-fg'
                      }`}
                    >
                      {entry.name}
                    </span>
                    <EnvironmentTag environment={entry.environment} />
                    <LinkStrand state={strandState(entry)} className="w-10 shrink-0" />
                  </button>
                </li>
              )
            })}
          </ul>

          <div className="mt-1.5 border-t border-line-soft pt-1.5">
            <button
              type="button"
              role="menuitem"
              data-active={cursor === clusters.length}
              onMouseEnter={() => setCursor(clusters.length)}
              onClick={backToFleet}
              className={`flex w-full items-center gap-2.5 rounded-control px-2.5 py-2 text-left text-[13px] transition-colors ${
                cursor === clusters.length ? 'bg-raised text-fg' : 'text-muted hover:bg-raised hover:text-fg'
              }`}
            >
              <Layers aria-hidden="true" className="size-3.5 shrink-0" />
              Back to the whole fleet
            </button>
          </div>
        </div>
      ) : null}
    </div>
  )
}
