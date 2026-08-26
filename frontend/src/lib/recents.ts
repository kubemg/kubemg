import { useEffect, useState } from 'react'

/**
 * The clusters this browser last opened for work.
 *
 * A developer's fleet page is a launcher, and a launcher's most useful row is
 * not the first cluster alphabetically — it is the one they were in twenty
 * minutes ago. That is the only thing this answers.
 *
 * It follows `lib/favorites.ts` exactly, for the same reasons and with the same
 * two decisions worth restating:
 *
 *   - **It lives in the browser.** Where somebody was last means nothing to
 *     anyone else, nothing enforces it, and nothing on the server reads it — so
 *     a table, a route and a round trip in front of a landing page would be a
 *     real cost for a preference whose whole blast radius is one browser.
 *   - **It is a module-level store with subscribers**, not component state. The
 *     fleet page and any future surface drawing "where you were" must not
 *     disagree, and a write happens on a different page from every read.
 *
 * Ids are stored rather than names or whole clusters: a cluster can be renamed
 * or revoked, and the id is what resolves against the fleet the caller is
 * actually allowed to see. A recorded cluster the caller has since lost access
 * to simply does not resolve, which is the behaviour that needs no rule.
 */

const STORAGE_KEY = 'kubemg.recent_clusters'

/**
 * How many ids are kept. More than any surface draws, so that a caller
 * filtering the list against its own grant still has something left — the
 * fleet page shows three, and three of the six may be clusters this browser can
 * no longer open.
 */
const MAX_RECENTS = 8

const listeners = new Set<() => void>()
let recents = read()

function read(): number[] {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    if (!stored) return []
    const parsed: unknown = JSON.parse(stored)
    // Anything could be under that key — another version of this console, or a
    // hand-edited value. A malformed entry is dropped rather than trusted.
    if (!Array.isArray(parsed)) return []
    return parsed
      .filter((entry): entry is number => typeof entry === 'number' && Number.isInteger(entry))
      .slice(0, MAX_RECENTS)
  } catch {
    return []
  }
}

function write(next: number[]) {
  recents = next
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
  } catch {
    // Private browsing refuses storage; the order still holds for this session.
  }
  for (const listener of listeners) listener()
}

/** recentClusterIds is the stored order, newest first, for a non-component caller. */
export function recentClusterIds(): number[] {
  return recents
}

/**
 * Records that a cluster was opened. Moving an id already in the list to the
 * front rather than appending is the whole point — otherwise the list is the
 * order clusters were *first* opened, which stops changing after a week.
 *
 * A repeat of the cluster already at the front writes nothing at all: this is
 * called from an effect on a page whose other state moves constantly, and a
 * write per render would notify every subscriber for no change.
 */
export function recordRecentCluster(id: number) {
  if (recents[0] === id) return
  write([id, ...recents.filter((entry) => entry !== id)].slice(0, MAX_RECENTS))
}

/** useRecentClusters is the stored order as a hook, newest first. */
export function useRecentClusters(): number[] {
  const [ids, setIds] = useState(recents)

  useEffect(() => {
    setIds(recents)
    const listener = () => setIds(recents)
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }, [])

  return ids
}
