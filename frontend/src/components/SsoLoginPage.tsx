import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Building2, KeyRound, Network } from 'lucide-react'
import { errorMessage, fetchSSOProviders, ssoLoginURL, ssoPasswordLogin } from '../api/client'
import type { SSOProtocol, SSOProviderSummary } from '../api/types'
import { Button, Field, Notice, Spinner, TextInput } from './primitives'
import { useAuth } from '../state/auth-context'

/*
 * The identity providers on the sign-in page.
 *
 * Two shapes, because there are two kinds of provider and pretending otherwise
 * would make one of them lie. An OIDC or SAML provider is a button: the browser
 * leaves for the IdP and comes back with a session. An LDAP directory has no
 * redirect at all — it takes a username and a password on this form — so it
 * expands in place instead of pretending to send anyone anywhere.
 *
 * The whole block is absent when nothing is configured, which is the ordinary
 * state of a fresh install: an empty "or sign in with" divider above nothing is
 * worse than no divider.
 */

const PROTOCOL_ICON: Record<SSOProtocol, typeof KeyRound> = {
  oidc: KeyRound,
  saml: Building2,
  ldap: Network,
}

export function SsoLoginPage({ onBusyChange }: { onBusyChange?: (busy: boolean) => void }) {
  const [providers, setProviders] = useState<SSOProviderSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [active, setActive] = useState<SSOProviderSummary | null>(null)

  useEffect(() => {
    let mounted = true
    fetchSSOProviders()
      // An install with no providers and one whose server is briefly unreachable
      // both land here as "no providers": the password form still works, and a
      // real outage announces itself on the next request with a better message.
      .catch(() => [] as SSOProviderSummary[])
      .then((next) => {
        if (mounted) {
          setProviders(next)
          setLoading(false)
        }
      })
    return () => {
      mounted = false
    }
  }, [])

  if (loading) {
    return (
      <p className="mt-6 flex items-center gap-2 text-[12.5px] text-muted">
        <Spinner className="size-3.5" />
        Checking for single sign-on…
      </p>
    )
  }
  if (providers.length === 0) return null

  return (
    <div className="mt-6">
      <div className="flex items-center gap-3">
        <span aria-hidden="true" className="h-px flex-1 bg-line" />
        <span className="label">or continue with</span>
        <span aria-hidden="true" className="h-px flex-1 bg-line" />
      </div>

      <div className="mt-4 flex flex-col gap-2">
        {providers.map((provider) => {
          const Icon = PROTOCOL_ICON[provider.protocol]
          const expanded = active?.id === provider.id

          return (
            <div key={provider.id} className="flex flex-col gap-2">
              <Button
                type="button"
                variant="ghost"
                className="h-10 w-full justify-between"
                aria-expanded={provider.interactive ? undefined : expanded}
                onClick={() => {
                  if (provider.interactive) {
                    // A full navigation, not fetch: the IdP answers with its own
                    // login page, which has to render in the address bar the
                    // person can see.
                    window.location.href = ssoLoginURL(provider.id)
                    return
                  }
                  setActive(expanded ? null : provider)
                }}
              >
                <span className="flex min-w-0 items-center gap-2.5">
                  <Icon aria-hidden="true" className="size-4 shrink-0 text-muted" />
                  <span className="truncate">{provider.name}</span>
                </span>
                <span className="flex shrink-0 items-center gap-2">
                  <span className="label">{provider.protocol}</span>
                  {provider.interactive ? (
                    <ArrowRight aria-hidden="true" className="size-4 text-faint" />
                  ) : null}
                </span>
              </Button>

              {expanded ? (
                <DirectoryForm provider={provider} onBusyChange={onBusyChange} />
              ) : null}
            </div>
          )
        })}
      </div>
    </div>
  )
}

/**
 * The LDAP form. It posts to the provider rather than to the local login route,
 * so a directory account and a local account with the same name stay two
 * different things — which is exactly what the server enforces on the way in.
 */
function DirectoryForm({
  provider,
  onBusyChange,
}: {
  provider: SSOProviderSummary
  onBusyChange?: (busy: boolean) => void
}) {
  const { adoptSession } = useAuth()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    onBusyChange?.(true)
    setError(null)
    try {
      const session = await ssoPasswordLogin(provider.id, username, password)
      await adoptSession(session.token, session.user)
    } catch (err) {
      setError(errorMessage(err, 'Sign in failed.'))
      setBusy(false)
      onBusyChange?.(false)
    }
  }

  return (
    <form
      onSubmit={submit}
      className="flex flex-col gap-3 rounded-control border border-line bg-raised p-3"
    >
      <Field label="Directory username" htmlFor={`sso-user-${provider.id}`}>
        <TextInput
          id={`sso-user-${provider.id}`}
          autoComplete="username"
          autoFocus
          required
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </Field>
      <Field label="Password" htmlFor={`sso-pass-${provider.id}`}>
        <TextInput
          id={`sso-pass-${provider.id}`}
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </Field>

      {error ? <Notice tone="error">{error}</Notice> : null}

      <Button type="submit" variant="primary" disabled={busy} className="h-9 w-full">
        {busy ? 'Signing in…' : `Sign in with ${provider.name}`}
      </Button>
    </form>
  )
}
