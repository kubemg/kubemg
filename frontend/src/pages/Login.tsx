import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Moon, Sun } from 'lucide-react'
import { Button, Field, Notice, TextInput } from '../components/primitives'
import { Lockup } from '../components/Mark'
import { EnvironmentBanner } from '../components/EnvironmentBanner'
import { useBranding } from '../state/branding-context'
import { SsoProviderSlot } from '../components/SsoProviderSlot'
import { useSsoProviders } from '../lib/useSsoProviders'
import { useAuth } from '../state/auth-context'
import { readSignInFailure, signInErrorText } from '../lib/signInError'
import { useTheme } from '../lib/theme'

export function Login() {
  const { signIn, setupRequired } = useAuth()
  const { theme, toggle } = useTheme()
  const providers = useSsoProviders()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await signIn(username, password)
    } catch (err) {
      // The console's words, never the server's: see lib/signInError for why a
      // wrong password and an unknown username must read the same.
      setError(
        signInErrorText(readSignInFailure(err), 'local', {
          hasProviders: (providers?.length ?? 0) > 0,
        }),
      )
      setBusy(false)
    }
  }

  return (
    /* The banner is above everything, and it is the reason the branding read
       happens outside the auth gate: an operator has to be able to tell a
       production console from a staging one *before* typing a password into
       it, not after. */
    <div className="flex min-h-svh flex-col">
      <EnvironmentBanner />
      <main className="grid flex-1 lg:grid-cols-[1.1fr_minmax(420px,0.9fr)]">
      {/* The left half is the product's own words: no inbound ports, every
          call on the record, and nothing here needs to prove that with a
          diagram. */}
      <section className="relative hidden flex-col justify-between overflow-hidden bg-rail p-10 lg:flex">
        {/* Texture, not signal — a static field standing in for the fleet, and
            a soft accent glow anchoring the corner. Nothing here animates: the
            deck's one moving mark is the breath on a live link, and this page
            has no cluster to draw one for. */}
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0"
          style={{
            backgroundImage: 'radial-gradient(circle, var(--deck-rail-border) 1px, transparent 1px)',
            backgroundSize: '22px 22px',
          }}
        />
        <div
          aria-hidden="true"
          className="pointer-events-none absolute -right-28 -bottom-28 size-[480px] rounded-full"
          style={{
            /* A haze of the accent itself, not `accent-soft`. That token is a
               fill meant to sit under text, so as a gradient it reads as a
               dirty olive smudge on both decks rather than as lime light. */
            backgroundImage:
              'radial-gradient(circle, color-mix(in oklab, var(--deck-accent-fill) 20%, transparent), transparent 70%)',
          }}
        />

        {/* h-9: the same height as the deck toggle opposite, so the two halves
            share a top rail. */}
        <div className="relative flex h-9 items-center gap-3">
          <Lockup className="text-[22px] text-rail-fg" />
          {/* Beside the lockup, never instead of it. A console that presents
              itself wholly as somebody else's product is one nobody can get
              support for — and the organisation's own mark is what makes it
              theirs to introduce. */}
          <OrganisationIdentity tone="rail" />
        </div>

        <div className="relative max-w-md">
          <h1 className="text-[34px] leading-[1.1] font-bold tracking-[-0.03em] text-rail-fg">
            No inbound ports.
            <br />
            Every call on the record.
          </h1>
          <p className="mt-4 text-[14px] leading-relaxed text-rail-muted">
            Every cluster holds an outbound tunnel to kubemg. Access is issued here, and kubectl
            traffic is proxied under your own identity.
          </p>
        </div>

        <p className="relative text-[12px] text-rail-faint">
          kubemg · centralized Kubernetes access
        </p>
      </section>

      {/* The right half keeps the left half's three rails — a top row, the
          content, a mono line at the foot — on one 400px column, so the toggle
          sits on the card's right edge, the footnote on its left, and the card
          is centred between two things rather than floating in what was left
          over. The toggle is in the flow rather than fixed, so it can never sit
          on top of the environment banner. */}
      <section className="flex flex-col bg-bg p-6 lg:p-10">
        <div className="mx-auto flex w-full max-w-[400px] flex-1 flex-col">
          <div className="flex h-9 items-center justify-end">
            <button
              type="button"
              onClick={toggle}
              title={theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
              className="grid size-9 place-items-center rounded-control border border-line bg-surface text-muted transition-colors hover:bg-raised hover:text-fg"
            >
              {theme === 'dark' ? (
                <Sun aria-hidden="true" className="size-4" />
              ) : (
                <Moon aria-hidden="true" className="size-4" />
              )}
              <span className="sr-only">
                {theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
              </span>
            </button>
          </div>

          <div className="flex flex-1 items-center py-8">
            <div data-testid="sign-in-card" className="card lift w-full p-8">
              {/* Below `lg` the brand panel beside this card is gone, so the card
                  carries the lockup itself. */}
              <div className="mb-7 flex items-center gap-3 lg:hidden">
                <Lockup className="text-[20px] text-fg" />
                <OrganisationIdentity tone="page" />
              </div>

              <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Sign in</h2>
              <p className="mt-1.5 text-[13px] text-muted">
                {setupRequired
                  ? 'This bastion has not been set up yet. Sign in as the administrator to configure it.'
                  : 'Use the account your administrator issued.'}
              </p>

              {/* Said here rather than after the sign-in, because the password
                  somebody needs is in a place they may have to go and look. */}
              {setupRequired ? (
                <div className="mt-4">
                  <Notice tone="info">
                    With no administrator password configured, one was generated on first boot and
                    printed once to the server’s log — <span className="font-mono">docker compose logs
                    kubemg</span> on a compose install. Setup makes changing it the first step.
                  </Notice>
                </div>
              ) : null}

              <form onSubmit={handleSubmit} className="mt-6 flex flex-col gap-4">
                <Field label="Username" htmlFor="username">
                  <TextInput
                    id="username"
                    name="username"
                    autoComplete="username"
                    autoFocus
                    required
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                  />
                </Field>

                <Field label="Password" htmlFor="password">
                  <TextInput
                    id="password"
                    name="password"
                    type="password"
                    autoComplete="current-password"
                    required
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                  />
                </Field>

                {error ? <Notice tone="error">{error}</Notice> : null}

                <Button type="submit" variant="primary" disabled={busy} className="mt-1 h-10 w-full">
                  {busy ? 'Signing in…' : 'Sign in'}
                  {busy ? null : <ArrowRight aria-hidden="true" className="size-4" />}
                </Button>
              </form>

              {/* Federated sign-in. The slot holds one provider's height whether
                  or not any is configured, so the card is the same size on every
                  install and does not grow once the read answers. */}
              <SsoProviderSlot providers={providers} onBusyChange={setBusy} />
            </div>
          </div>

          {/* The page's counterpart to the rail's footnote, and the thing a
              sign-in page at an enterprise is expected to say. */}
          <p className="text-[12px] text-faint">
            Cluster calls made through kubemg are recorded under your identity.
          </p>
        </div>
      </section>
      </main>
    </div>
  )
}

/**
 * The organisation's own mark and name, beside the lockup.
 *
 * It draws nothing when neither is configured, which is the default: an install
 * that has not been branded looks exactly as it did. The divider is only ever
 * drawn between two things that are both there.
 */
function OrganisationIdentity({ tone }: { tone: 'rail' | 'page' }) {
  const { branding } = useBranding()
  const name = branding?.organisation_name?.trim()
  const mark = branding?.organisation_mark?.trim()
  if (!name && !mark) return null

  const rule = tone === 'rail' ? 'bg-rail-line' : 'bg-line'
  const text = tone === 'rail' ? 'text-rail-muted' : 'text-muted'

  return (
    <>
      <span aria-hidden="true" className={`h-5 w-px shrink-0 ${rule}`} />
      {mark ? <img src={mark} alt="" className="size-6 shrink-0 object-contain" /> : null}
      {name ? <span className={`min-w-0 truncate text-[13px] ${text}`}>{name}</span> : null}
    </>
  )
}
