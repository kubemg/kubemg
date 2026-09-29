import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Building2, KeyRound, Network } from 'lucide-react'
import { ssoLoginURL, ssoPasswordLogin } from '../api/client'
import type { SSOProtocol, SSOProviderSummary } from '../api/types'
import { Button, Field, Notice, TextInput } from './primitives'
import { readSignInFailure, signInErrorText } from '../lib/signInError'
import { useAuth } from '../state/auth-context'

/*
 * The identity providers on the sign-in page.
 *
 * Two shapes, because there are two kinds of provider and pretending otherwise
 * would make one of them lie. An OIDC or SAML provider is a link: the browser
 * leaves for the IdP and comes back with a session. An LDAP directory has no
 * redirect at all — it takes a username and a password on this form — so it
 * expands in place instead of pretending to send anyone anywhere.
 *
 * The slot holds its place whether or not anything fills it. An enterprise
 * buyer's first question is whether this federates, and a button that arrives
 * after the read — or only on the install that configured one — shifts the card
 * under the cursor and makes the answer look bolted on. So the slot is exactly
 * one provider tall (a 16px divider row, 16px, a 40px button: 72px) with none,
 * while the read is in flight, and with one; only a second provider grows it.
 * Empty, it draws nothing at all — no divider over a blank, no placeholder.
 */

const PROTOCOL_ICON: Record<SSOProtocol, typeof KeyRound> = {
  oidc: KeyRound,
  saml: Building2,
  ldap: Network,
}

/** The providers come from `useSsoProviders`; null means the read is in flight. */
export function SsoProviderSlot({
  providers,
  onBusyChange,
}: {
  providers: SSOProviderSummary[] | null
  onBusyChange?: (busy: boolean) => void
}) {
  const [active, setActive] = useState<SSOProviderSummary | null>(null)
  const shown = providers ?? []

  return (
    <div data-slot="sso-providers" className="mt-6 min-h-18">
      {shown.length > 0 ? (
        <>
          <div className="flex h-4 items-center gap-3">
            <span aria-hidden="true" className="h-px flex-1 bg-line" />
            <span className="label">or continue with</span>
            <span aria-hidden="true" className="h-px flex-1 bg-line" />
          </div>

          <div className="mt-4 flex flex-col gap-2">
            {shown.map((provider) => {
              const Icon = PROTOCOL_ICON[provider.protocol]
              const expanded = active?.id === provider.id
              const face = (
                <>
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
                </>
              )

              return (
                <div key={provider.id} className="flex flex-col gap-2">
                  {provider.interactive ? (
                    // A full navigation, not fetch: the IdP answers with its own
                    // login page, which has to render in the address bar the
                    // person can see.
                    <a
                      href={ssoLoginURL(provider.id)}
                      className="inline-flex h-10 w-full items-center justify-between gap-2 rounded-control px-3.5 text-[13.5px] font-medium whitespace-nowrap text-muted transition-colors hover:bg-raised hover:text-fg"
                    >
                      {face}
                    </a>
                  ) : (
                    <Button
                      type="button"
                      variant="ghost"
                      className="h-10 w-full justify-between"
                      aria-expanded={expanded}
                      onClick={() => setActive(expanded ? null : provider)}
                    >
                      {face}
                    </Button>
                  )}

                  {expanded ? (
                    <DirectoryForm provider={provider} onBusyChange={onBusyChange} />
                  ) : null}
                </div>
              )
            })}
          </div>
        </>
      ) : null}
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
      setError(signInErrorText(readSignInFailure(err), 'directory'))
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
