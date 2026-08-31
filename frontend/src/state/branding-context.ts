import { createContext, useContext } from 'react'
import type { Branding } from '../api/types'

export interface BrandingState {
  /** Null while the read is in flight, and an empty object once it has answered
      with nothing configured. The two are distinguished so that a banner never
      appears a beat after the page it belongs to — a strip that arrives late
      pushes the whole console down under the operator's cursor. */
  branding: Branding | null
  /** Called by the settings form so a save shows up everywhere at once, rather
      than only after a reload. */
  refresh: () => Promise<void>
}

export const BrandingContext = createContext<BrandingState | null>(null)

export function useBranding(): BrandingState {
  const state = useContext(BrandingContext)
  if (!state) {
    throw new Error('useBranding must be used inside <BrandingProvider>')
  }
  return state
}
