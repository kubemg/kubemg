import { createContext, useContext } from 'react'
import type { VulnWorkloadSummary } from '../api/types'
import { vulnBadgeText, vulnBadgeTitle, vulnKey, vulnTone } from '../lib/vulnFindings'
import { Pill } from './primitives'

/**
 * The findings summary for the list on screen, keyed by `vulnKey`. Null —
 * the default — means nobody here may see findings, and every badge is absent.
 */
export const VulnIndexContext = createContext<ReadonlyMap<string, VulnWorkloadSummary> | null>(null)

/**
 * A workload row's badge: only where the level carries a colour. A row whose
 * findings have no fix waiting draws nothing — the count is on the page and in
 * the drawer, and a badge on every row would be the permanent red this exists
 * to avoid.
 */
export function VulnBadge({ kind, namespace, name }: { kind: string; namespace?: string; name: string }) {
  const index = useContext(VulnIndexContext)
  if (!index || !namespace) return null
  const row = index.get(vulnKey(kind, namespace, name))
  if (!row) return null
  const tone = vulnTone(row.level)
  if (!tone) return null
  return (
    <span className="mt-1 flex pl-4">
      <Pill tone={tone} title={vulnBadgeTitle(row)}>
        {vulnBadgeText(row)}
      </Pill>
    </span>
  )
}
