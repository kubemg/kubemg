import { useEffect, useMemo, useState } from 'react'
import type { ResourceKey } from './resources'

/**
 * The resource rows an operator pinned to the top of the cluster tree.
 *
 * Everybody browses a different corner of a cluster: one person lives in Pods
 * and HTTPRoutes, the next in CronJobs and ConfigMaps, and both of those are
 * three or four rows scattered across a column that a cluster with a few
 * operators can run to fifty. Favourites are the answer to that and nothing
 * more — they are **navigation, not a view**: a favourited row is the same link
 * to the same list, drawn a second time where it can be reached without
 * scrolling, and starring one takes nothing off the tree below.
 *
 * Two decisions worth stating, because both could reasonably have gone the
 * other way:
 *
 *   - **It lives in the browser**, like the deck and the live switch, rather
 *     than on the server. What somebody pinned means nothing to anyone else,
 *     nothing enforces it, and nothing else needs to read it — so a table, a
 *     route and a round trip in front of the sidebar would be a real cost for
 *     a preference whose entire blast radius is one column of one browser.
 *   - **The set is one set, not one per cluster.** `pods` means the same thing
 *     on every cluster, and somebody who pinned Pods wants them pinned when
 *     they switch. A `crd:` key is the interesting case, and it needs no rule
 *     of its own: the group is built from *this* cluster's own inventory, so a
 *     kind the cluster does not serve simply does not appear — exactly what
 *     `resourceItem` already does with a key from another cluster.
 */

const STORAGE_KEY = 'kubemg.favorites'

const listeners = new Set<() => void>()
let pinned = read()

function read(): ResourceKey[] {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    if (!stored) return []
    const parsed: unknown = JSON.parse(stored)
    // Anything could be under that key — another version of this console, or a
    // hand-edited value. A malformed entry is dropped rather than trusted,
    // because what it would reach is a resource key the tree looks kinds up by.
    if (!Array.isArray(parsed)) return []
    return parsed.filter((entry): entry is ResourceKey => typeof entry === 'string')
  } catch {
    return []
  }
}

function write(next: ResourceKey[]) {
  pinned = next
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
  } catch {
    // Private browsing refuses storage; the choice still holds for this session.
  }
  for (const listener of listeners) listener()
}

/** favoriteKeys is the stored set, for a caller that is not a component. */
export function favoriteKeys(): ResourceKey[] {
  return pinned
}

export function toggleFavorite(key: ResourceKey) {
  write(pinned.includes(key) ? pinned.filter((entry) => entry !== key) : [...pinned, key])
}

/**
 * useFavorites reads the set and toggles it. It is a module-level store with
 * subscribers rather than component state for the reason `useLive` is: two
 * places can draw the same star — a row in its own section and the same row
 * pinned at the top — and they must never disagree about whether it is filled.
 */
export function useFavorites(): {
  favorites: ReadonlySet<ResourceKey>
  toggle: (key: ResourceKey) => void
} {
  const [keys, setKeys] = useState(pinned)

  useEffect(() => {
    setKeys(pinned)
    const listener = () => setKeys(pinned)
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }, [])

  // Memoised on the stored array, whose identity only changes on a write: the
  // tree builds its groups from this set, and a fresh set every render would
  // rebuild them on every render.
  const favorites = useMemo(() => new Set(keys), [keys])

  return { favorites, toggle: toggleFavorite }
}
