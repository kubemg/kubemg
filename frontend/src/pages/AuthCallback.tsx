import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { errorMessage } from '../api/client'
import { Notice, Spinner } from '../components/primitives'
import { Mark } from '../components/Mark'
import { useAuth } from '../state/auth-context'

/**
 * Where an interactive sign-in lands on the way back from an identity provider.
 *
 * The session arrives in the URL *fragment* rather than the query string,
 * because a fragment is never sent to a server: a token in the query would be in
 * the access log of every proxy in front of this page and in the browser's own
 * history. It is read once, stored, and the fragment is stripped from the
 * address bar immediately — a page left open on a URL containing a live session
 * is a session someone can copy out of a screen share.
 */
export function AuthCallback() {
  const { adoptSession, user } = useAuth()
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const params = new URLSearchParams(window.location.hash.replace(/^#/, ''))
    const token = params.get('token')
    const failure = params.get('error')

    // Whatever happens next, this must not stay in the address bar.
    window.history.replaceState(null, '', window.location.pathname)

    if (failure) {
      setError(failure)
      return
    }
    if (!token) {
      setError('This sign-in did not return a session. Please try again.')
      return
    }

    adoptSession(token).catch((err) => {
      setError(errorMessage(err, 'That session could not be used. Please sign in again.'))
    })
  }, [adoptSession])

  // The router sends an authenticated caller on; until then this is the only
  // thing on screen, so it says which of the two states it is in.
  if (user) return null

  return (
    <main className="flex min-h-svh flex-col items-center justify-center gap-4 bg-bg px-6">
      <span className="flex items-center gap-2.5">
        <Mark className="size-7 shrink-0 text-accent" />
        <span className="text-[18px] font-semibold tracking-[-0.02em]">
          <span className="text-fg">Kube</span>
          <span className="text-accent">MG</span>
        </span>
      </span>

      {error ? (
        <div className="flex w-full max-w-[420px] flex-col gap-3">
          <Notice tone="error">{error}</Notice>
          <Link
            to="/login"
            className="inline-flex h-9 items-center justify-center rounded-control border border-line bg-surface text-[13.5px] font-medium text-fg transition-colors hover:border-faint/60 hover:bg-raised"
          >
            Back to sign in
          </Link>
        </div>
      ) : (
        <p className="flex items-center gap-2 text-[13px] text-muted">
          <Spinner className="size-3.5" />
          Completing sign-in…
        </p>
      )}
    </main>
  )
}
