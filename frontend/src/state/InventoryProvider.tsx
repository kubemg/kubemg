import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { useLocation } from 'react-router'
import { fetchCRDs } from '../api/client'
import type { CustomResourceDefinition } from '../api/types'
import { clusterIdFromPath, hasTunnel } from '../lib/navigation'
import { discoverCategories, exploreCategories } from '../lib/resources'
import { useClusters } from './clusters-context'
import { InventoryContext } from './inventory-context'
import type { InventoryState } from './inventory-context'

/**
 * InventoryProvider is what the open cluster can browse, read once per cluster
 * rather than once per page.
 *
 * It used to live inside Explore, which was correct while Explore was the only
 * page with a resource tree beside it. The tree is now the console's second
 * level of navigation and is drawn on every one of a cluster's pages, so the
 * inventory has to outlive any one of them — otherwise the same cluster would
 * offer a different tree depending on which of its pages you happened to be on.
 *
 * Answers are cached per cluster for the session. Which CRDs a cluster has
 * changes only when somebody installs an operator, and re-reading them on every
 * move between two of its pages would spend a tunnel round trip to redraw the
 * navigation identically.
 */
export function InventoryProvider({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  const { clusters } = useClusters()

  const clusterId = clusterIdFromPath(pathname)
  const cluster = clusterId !== null ? clusters.find((entry) => entry.id === clusterId) : undefined
  // Only a cluster with a tunnel can be asked. One without keeps the fixed
  // inventory, which is also exactly what its tree draws as unreachable.
  const readable = cluster && hasTunnel(cluster) ? cluster : undefined

  const cache = useRef(new Map<number, CustomResourceDefinition[]>())
  const [crds, setCrds] = useState<CustomResourceDefinition[] | null>(null)

  useEffect(() => {
    if (!readable) {
      setCrds([])
      return
    }

    const cached = cache.current.get(readable.id)
    if (cached) {
      setCrds(cached)
      return
    }

    let cancelled = false
    setCrds(null)

    fetchCRDs(readable.id)
      .then((result) => {
        cache.current.set(readable.id, result)
        if (!cancelled) setCrds(result)
      })
      .catch(() => {
        // A namespace-scoped grant cannot list CRDs cluster-wide, and that is a
        // legitimate answer: the tree keeps its fixed inventory. It is cached
        // like any other, so the refusal is not re-asked on every navigation.
        cache.current.set(readable.id, [])
        if (!cancelled) setCrds([])
      })

    return () => {
      cancelled = true
    }
  }, [readable])

  const value = useMemo<InventoryState>(() => {
    const discovered = discoverCategories(crds ?? [])
    return {
      discovered,
      categories: exploreCategories(discovered),
      ready: crds !== null,
    }
  }, [crds])

  return <InventoryContext.Provider value={value}>{children}</InventoryContext.Provider>
}
