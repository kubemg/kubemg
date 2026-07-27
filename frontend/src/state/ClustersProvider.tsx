import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { errorMessage, fetchClusters } from '../api/client'
import type { Cluster } from '../api/types'
import { ClustersContext } from './clusters-context'
import type { ClustersState } from './clusters-context'

export function ClustersProvider({ children }: { children: ReactNode }) {
  const [clusters, setClusters] = useState<Cluster[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selectedId, setSelectedId] = useState<number | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      const next = await fetchClusters()
      setClusters(next)
      setError(null)
      // Keep the selection meaningful: hold it if it survived, otherwise fall
      // back to the first cluster the user can reach.
      setSelectedId((current) =>
        current !== null && next.some((cluster) => cluster.id === current)
          ? current
          : (next[0]?.id ?? null),
      )
    } catch (err) {
      setError(errorMessage(err, 'Could not load clusters.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  const value = useMemo<ClustersState>(
    () => ({
      clusters,
      loading,
      error,
      selected: clusters.find((cluster) => cluster.id === selectedId) ?? null,
      select: setSelectedId,
      reload,
    }),
    [clusters, loading, error, selectedId, reload],
  )

  return <ClustersContext.Provider value={value}>{children}</ClustersContext.Provider>
}
