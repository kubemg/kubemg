import { createContext, useContext } from 'react'
import type { ResourceCategory } from '../lib/resources'

export interface InventoryState {
  /** Every category the open cluster can browse: the fixed inventory plus
      whatever its own CRDs added. */
  categories: ResourceCategory[]
  /** Only the categories discovered from this cluster's CRDs, which is what
      resolves a `crd:` resource key. */
  discovered: ResourceCategory[]
  /**
   * Whether discovery has answered for the open cluster. A `crd:` key cannot be
   * resolved before it has, and "not answered yet" is a different situation from
   * "this cluster does not have it" — the first waits, the second falls back.
   */
  ready: boolean
}

export const InventoryContext = createContext<InventoryState | null>(null)

export function useInventory(): InventoryState {
  const state = useContext(InventoryContext)
  if (!state) {
    throw new Error('useInventory must be used inside <InventoryProvider>')
  }
  return state
}
