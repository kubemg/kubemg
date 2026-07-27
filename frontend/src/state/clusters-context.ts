import { createContext, useContext } from 'react'
import type { Cluster } from '../api/types'

export interface ClustersState {
  clusters: Cluster[]
  loading: boolean
  error: string | null
  /** The cluster the header selector is pointing at. */
  selected: Cluster | null
  select: (id: number) => void
  reload: () => Promise<void>
}

export const ClustersContext = createContext<ClustersState | null>(null)

export function useClusters(): ClustersState {
  const state = useContext(ClustersContext)
  if (!state) {
    throw new Error('useClusters must be used inside <ClustersProvider>')
  }
  return state
}
