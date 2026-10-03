import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { CornerDownLeft, Search } from 'lucide-react'
import { useNavigate } from 'react-router'
import type { Cluster } from '../api/types'
import { EnvironmentDot, KeyHint } from './primitives'
import { LinkStatus } from './LinkStatus'
import { clusterHref, clusterPageHref, hasTunnel, resourceHref } from '../lib/navigation'
import { linkState } from '../lib/status'

export interface CommandTarget {
  id: string
  label: string
  hint: string
  to: string
  cluster?: Cluster
}

/**
 * A cluster's own views, addressed directly rather than through its default
 * landing: someone who wants that cluster's audit trail should not have to jump
 * to its dashboard first and click again.
 *
 * The pages that read through the tunnel are offered only where there is one,
 * and so are the two resource lists — the tree applies the same rule, and an
 * entry that can only refuse is worse than no entry. Pods and Deployments are
 * the two kinds worth a palette row of their own: they are where most of a
 * developer's day is spent, and every other kind is one keystroke away in the
 * tree once the cluster is open.
 */
function clusterViewTargets(cluster: Cluster): CommandTarget[] {
  const views: CommandTarget[] = [
    {
      id: `cluster-${cluster.id}-dashboard`,
      label: `${cluster.name} — Dashboard`,
      hint: 'Cluster · Dashboard',
      to: clusterPageHref(cluster.id, 'dashboard'),
      cluster,
    },
  ]
  if (hasTunnel(cluster)) {
    for (const kind of ['pods', 'deployments'] as const) {
      views.push({
        id: `cluster-${cluster.id}-${kind}`,
        label: `${cluster.name} — ${kind === 'pods' ? 'Pods' : 'Deployments'}`,
        hint: `Cluster · ${kind === 'pods' ? 'Pods' : 'Deployments'}`,
        to: resourceHref(cluster.id, kind),
        cluster,
      })
    }
    // "What broke" is a question people arrive with, not one they navigate to,
    // and arriving means the palette as often as the tree. Same for "why will
    // nothing schedule".
    views.push({
      id: `cluster-${cluster.id}-events`,
      label: `${cluster.name} — Events`,
      hint: 'Cluster · Events',
      to: clusterPageHref(cluster.id, 'events'),
      cluster,
    })
    views.push({
      id: `cluster-${cluster.id}-alerts`,
      label: `${cluster.name} — Alerts`,
      hint: 'Cluster · Alerts',
      to: clusterPageHref(cluster.id, 'alerts'),
      cluster,
    })
    views.push({
      id: `cluster-${cluster.id}-capacity`,
      label: `${cluster.name} — Capacity`,
      hint: 'Cluster · Capacity',
      to: clusterPageHref(cluster.id, 'capacity'),
      cluster,
    })
    views.push({
      id: `cluster-${cluster.id}-security`,
      label: `${cluster.name} — Security posture`,
      hint: 'Cluster · Security posture',
      to: clusterPageHref(cluster.id, 'security'),
      cluster,
    })
  }
  views.push({
    id: `cluster-${cluster.id}-audit`,
    label: `${cluster.name} — Audit trail`,
    hint: 'Cluster · Audit trail',
    to: clusterPageHref(cluster.id, 'audit'),
    cluster,
  })
  return views
}

/**
 * CommandPalette is how an operator moves around a fleet: type part of a cluster
 * name or a page and go. It is the only navigation that scales past a screenful
 * of clusters, so it is reachable from anywhere with ⌘K.
 */
export function CommandPalette({
  open,
  onClose,
  pages,
  clusters,
}: {
  open: boolean
  onClose: () => void
  pages: CommandTarget[]
  clusters: Cluster[]
}) {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const listRef = useRef<HTMLUListElement | null>(null)
  const listId = useId()

  const targets = useMemo<CommandTarget[]>(
    () => [
      ...clusters.map((cluster) => ({
        id: `cluster-${cluster.id}`,
        label: cluster.name,
        hint: cluster.connection_mode === 'agent' ? 'Cluster · agent' : 'Cluster · direct',
        // The same rule as the fleet list and the rail: a cluster with a
        // tunnel opens on its resources, one without opens on its dashboard.
        to: clusterHref(cluster),
        cluster,
      })),
      // A cluster's own views, so ⌘K reaches Summary, Explore and Audit trail
      // directly rather than only the cluster's default landing above.
      ...clusters.flatMap((cluster) => clusterViewTargets(cluster)),
      ...pages,
    ],
    [clusters, pages],
  )

  const needle = query.trim().toLowerCase()
  const matches = needle
    ? targets.filter(
        (target) =>
          target.label.toLowerCase().includes(needle) ||
          target.hint.toLowerCase().includes(needle),
      )
    : targets

  useEffect(() => {
    if (open) {
      setQuery('')
      setCursor(0)
    }
  }, [open])

  useEffect(() => {
    setCursor(0)
  }, [needle])

  // Keep the highlighted row in view when walking a long fleet with the keyboard.
  useEffect(() => {
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [cursor])

  if (!open) return null

  function go(target: CommandTarget | undefined) {
    if (!target) return
    onClose()
    navigate(target.to)
  }

  function onKeyDown(event: React.KeyboardEvent) {
    if (event.key === 'Escape') {
      onClose()
      return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setCursor((current) => (matches.length === 0 ? 0 : (current + 1) % matches.length))
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setCursor((current) =>
        matches.length === 0 ? 0 : (current - 1 + matches.length) % matches.length,
      )
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      go(matches[cursor])
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex justify-center px-4 pt-[12vh]">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="scrim-in absolute inset-0 bg-scrim backdrop-blur-[2px]"
      />

      <div
        role="dialog"
        aria-modal="true"
        aria-label="Jump to"
        onKeyDown={onKeyDown}
        className="pop-in relative flex max-h-[60vh] w-full max-w-[600px] flex-col overflow-hidden rounded-card border border-line bg-surface shadow-lift"
      >
        <div className="flex items-center gap-3 border-b border-line-soft px-5">
          <Search aria-hidden="true" className="size-4.5 shrink-0 text-faint" />
          <input
            autoFocus
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={matches[cursor] ? `${listId}-${cursor}` : undefined}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Jump to a cluster or a page…"
            aria-label="Jump to a cluster or a page"
            name="palette-query"
            autoComplete="off"
            spellCheck={false}
            className="h-14 min-w-0 flex-1 bg-transparent text-[15px] text-fg placeholder:text-faint focus:outline-none"
          />
          <KeyHint>esc</KeyHint>
        </div>

        <ul
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label="Destinations"
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2"
        >
          {matches.map((target, index) => {
            const active = index === cursor
            // A cluster's own view is labelled "name — View": the name is an
            // identifier and set as data, the view is a word and is not.
            const [name, view] = target.cluster ? target.label.split(' — ') : [target.label]
            return (
              <li key={target.id} role="presentation">
                <button
                  type="button"
                  id={`${listId}-${index}`}
                  role="option"
                  aria-selected={active}
                  tabIndex={-1}
                  data-active={active}
                  onMouseEnter={() => setCursor(index)}
                  onClick={() => go(target)}
                  className={`flex w-full items-center gap-3 rounded-control px-3 py-2.5 text-left transition-colors duration-300 ${
                    active ? 'bg-accent-soft' : 'hover:bg-raised'
                  }`}
                >
                  {target.cluster ? (
                    <EnvironmentDot environment={target.cluster.environment} />
                  ) : (
                    <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-faint" />
                  )}

                  <span className="min-w-0 flex-1">
                    <span
                      className={`block truncate text-[14px] font-medium ${active ? 'text-accent' : 'text-fg'}`}
                    >
                      {target.cluster ? (
                        <>
                          <span className="font-data" translate="no">
                            {name}
                          </span>
                          {view ? ` — ${view}` : null}
                        </>
                      ) : (
                        target.label
                      )}
                    </span>
                    <span className="block truncate text-[12px] text-muted">{target.hint}</span>
                  </span>

                  {target.cluster ? (
                    <LinkStatus state={linkState(target.cluster)} variant="glyph" />
                  ) : null}

                  {active ? (
                    <CornerDownLeft aria-hidden="true" className="size-3.5 shrink-0 text-accent" />
                  ) : null}
                </button>
              </li>
            )
          })}

          {matches.length === 0 ? (
            <li role="presentation" className="px-3 py-6 text-center text-[13px] text-muted">
              Nothing matches “{query}”.
            </li>
          ) : null}
        </ul>
      </div>
    </div>
  )
}
