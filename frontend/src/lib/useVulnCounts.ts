import { useMemo } from 'react'
import { fetchVulnClusterCounts } from '../api/client'
import type { Cluster, VulnClusterCounts } from '../api/types'
import { queryKey, useCachedQuery } from './query'
import { mayAskForVulns } from './vulnFindings'

/**
 * Per-cluster vulnerability counts for the sidebar and the rail, keyed by
 * cluster id. A cluster absent from the map has no alerts the caller may see —
 * not connected, switched off, or outside the audience — and draws nothing.
 *
 * Asked once, and only when some cluster could answer; not live, for the
 * sidebar counts' reason. A failed read is silence.
 */
export function useVulnCounts(clusters: readonly Cluster[]): Map<number, VulnClusterCounts> {
  const ask = clusters.some(mayAskForVulns)
  const counts = useCachedQuery(ask ? queryKey('vuln-counts') : null, fetchVulnClusterCounts)
  return useMemo(() => {
    const index = new Map<number, VulnClusterCounts>()
    for (const entry of counts.data ?? []) index.set(entry.cluster_id, entry)
    return index
  }, [counts.data])
}
