import { useEffect, useState } from 'react'
import { fetchSSOProviders } from '../api/client'
import type { SSOProviderSummary } from '../api/types'

/**
 * The identity providers the sign-in page offers, or null while the read is in
 * flight. An install with none and one whose server is briefly unreachable both
 * read as none: the password form still works, and a real outage announces
 * itself on the next request with a better message.
 */
export function useSsoProviders(): SSOProviderSummary[] | null {
  const [providers, setProviders] = useState<SSOProviderSummary[] | null>(null)

  useEffect(() => {
    let mounted = true
    fetchSSOProviders()
      .catch(() => [] as SSOProviderSummary[])
      .then((next) => {
        if (mounted) setProviders(next)
      })
    return () => {
      mounted = false
    }
  }, [])

  return providers
}
