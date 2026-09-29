import { useEffect, useState } from 'react'
import { countExpiringKubeconfigs, fetchAudit } from '../api/client'
import { STRIP_WINDOW } from './fleetStrip'
import type { Count } from './fleetStrip'

/**
 * The strip's two remaining counts, each at the cheapest read its route allows:
 * a page of one, answered by the route's own `total`. Both are the question the
 * figure's link asks, so the number and the rows it opens onto agree.
 *
 * Read once, when the page opens or the role it is drawn for changes — never
 * on the live tick, which is the fleet list's and not these counts'. Each read
 * stands alone: one that fails is `null` for that figure and nothing else.
 *
 * `refused` is read for an administrator only. The trail would narrow anyone
 * else to their own calls, and the developer's body does not draw it.
 */
export function useFleetCounts(isAdmin: boolean): { refused: Count; expiring: Count } {
  const [refused, setRefused] = useState<Count>(undefined)
  const [expiring, setExpiring] = useState<Count>(undefined)

  useEffect(() => {
    let live = true
    setRefused(undefined)
    setExpiring(undefined)

    if (isAdmin) {
      void fetchAudit({ failed: true, range: STRIP_WINDOW, limit: 1 })
        .then((page) => live && setRefused(page.total))
        .catch(() => live && setRefused(null))
    }
    void countExpiringKubeconfigs(STRIP_WINDOW)
      .then((total) => live && setExpiring(total))
      .catch(() => live && setExpiring(null))

    return () => {
      live = false
    }
  }, [isAdmin])

  return { refused, expiring }
}
