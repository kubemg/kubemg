import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { fetchBranding } from '../api/client'
import type { Branding } from '../api/types'
import { BrandingContext } from './branding-context'

/**
 * BrandingProvider holds the customer's own identity for the whole console.
 *
 * It sits **above the auth gate**, not inside it, which is the one structural
 * decision here. An environment banner exists to be read before somebody types
 * a password into a console, so it has to render on the sign-in page — and that
 * page is drawn by a tree that has, by definition, no session. The read itself
 * is unauthenticated for the same reason.
 *
 * A failure reads as "nothing configured" rather than as an error. Everything
 * this provides is optional decoration on a page that works without it, and a
 * console that refuses to render because it could not load a logo would be a far
 * worse failure than one with no logo.
 */
export function BrandingProvider({ children }: { children: ReactNode }) {
  const [branding, setBranding] = useState<Branding | null>(null)

  const load = useCallback(async () => {
    try {
      setBranding(await fetchBranding())
    } catch {
      setBranding({})
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const value = useMemo(() => ({ branding, refresh: load }), [branding, load])

  return <BrandingContext.Provider value={value}>{children}</BrandingContext.Provider>
}
