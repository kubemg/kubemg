import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { errorMessage, fetchClusters } from '../api/client'
import type { Cluster } from '../api/types'
import { FLEET_INTERVAL, FLEET_PENDING_INTERVAL, useLiveTick } from '../lib/live'
import { sameAnswer } from '../lib/query'
import { ClustersContext } from './clusters-context'
import type { ClustersState } from './clusters-context'

/**
 * waiting is a cluster that has been registered and has not dialled in yet.
 *
 * It is the reason the fleet re-reads at all. Registering a cluster in agent
 * mode ends with an operator pasting a command into a terminal somewhere else,
 * and until the agent connects every surface here is honest but empty — an
 * Explore page with nothing to explore, a card with no link. The wizard's own
 * handshake step waits for exactly this, and a cluster registered from anywhere
 * else, or watched from another tab, deserves the same answer without somebody
 * reloading the browser to get it.
 */
function waiting(cluster: Cluster): boolean {
  return cluster.connection_mode === 'agent' ? !cluster.agent_attached : cluster.status === 'pending'
}

export function ClustersProvider({ children }: { children: ReactNode }) {
  const [clusters, setClusters] = useState<Cluster[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedId, setSelectedId] = useState<number | null>(null)

  /**
   * quiet is a re-read nobody asked for: the fleet list on its own cadence. It
   * differs from the loaded one in the two ways every background read here
   * does — it never puts the console back into its loading state, and a failure
   * leaves the fleet that is on screen alone rather than replacing a rail full
   * of clusters with an error because one poll missed. A real outage is reported
   * by the next read the operator does ask for.
   */
  const reload = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true)
    try {
      const next = await fetchClusters()
      // Identical answers are dropped rather than set: this list is context for
      // the whole console, so re-setting it would re-render every page under it
      // four times a minute to say nothing.
      setClusters((current) => (sameAnswer(current, next) ? current : next))
      setError(null)
      // Keep the selection meaningful: hold it if it survived, otherwise fall
      // back to the first cluster the user can reach.
      setSelectedId((current) =>
        current !== null && next.some((cluster) => cluster.id === current)
          ? current
          : (next[0]?.id ?? null),
      )
    } catch (err) {
      if (!quiet) setError(errorMessage(err, 'Could not load clusters.'))
    } finally {
      if (!quiet) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // Faster while something is still waiting to dial in, because that is the one
  // state where the answer is expected to change in the next few seconds. This
  // read never touches a cluster — it is KubeMG's own database plus the tunnel
  // registry — so it costs no impersonated call and writes no audit record.
  const pending = clusters.some(waiting)
  useLiveTick(
    useCallback(() => reload(true), [reload]),
    { interval: pending ? FLEET_PENDING_INTERVAL : FLEET_INTERVAL },
  )

  // The public reload is the loaded one, and its identity is stable: it is held
  // in a context every page reads, so a new function on every fleet change would
  // invalidate anything keyed on it.
  const load = useCallback(() => reload(), [reload])

  const value = useMemo<ClustersState>(
    () => ({
      clusters,
      loading,
      error,
      selected: clusters.find((cluster) => cluster.id === selectedId) ?? null,
      select: setSelectedId,
      reload: load,
    }),
    [clusters, loading, error, selectedId, load],
  )

  return <ClustersContext.Provider value={value}>{children}</ClustersContext.Provider>
}
