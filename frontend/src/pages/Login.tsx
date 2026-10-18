import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight } from 'lucide-react'
import type { MFAChallenge } from '../api/types'
import { SignInSecondStep } from '../components/MfaSteps'
import { Button, Field, Notice, TextInput } from '../components/primitives'
import { AuthLayout } from '../components/AuthLayout'
import { SsoProviderSlot } from '../components/SsoProviderSlot'
import { useSsoProviders } from '../lib/useSsoProviders'
import { useAuth } from '../state/auth-context'
import { readSignInFailure, signInErrorText } from '../lib/signInError'
import { returnTo } from '../lib/returnTo'

export function Login() {
  const { signIn, adoptSession, setupRequired } = useAuth()
  const providers = useSsoProviders()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // A right password with a second factor still owed: the card turns into the
  // code step, and no session exists until it is done.
  const [pending, setPending] = useState<{ challenge: MFAChallenge; account: string } | null>(null)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const challenge = await signIn(username, password)
      if (challenge) {
        setPassword('')
        setPending({ challenge, account: username })
        setBusy(false)
      }
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

  // Arriving from `kubemg login`: the page says so, so the walk from the
  // terminal through the sign-in to the approval reads as one errand.
  const forCLI = returnTo().startsWith('/cli/activate')

  return (
    <AuthLayout
      cardTestId="sign-in-card"
      headline={
        forCLI ? (
          <>
            A command line is waiting.
            <br />
            Sign in to approve it.
          </>
        ) : (
          <>
            No inbound ports.
            <br />
            Every call on the record.
          </>
        )
      }
      lede={
        forCLI
          ? 'kubemg login never sees your password or your second factor. Sign in here as usual, and approve the sign-in on the next page.'
          : 'Every cluster holds an outbound tunnel to kubemg. Access is issued here, and kubectl traffic is proxied under your own identity.'
      }
      footnote="Cluster calls made through kubemg are recorded under your identity."
    >
      {pending ? (
        <SignInSecondStep
          challenge={pending.challenge}
          account={pending.account}
          onSession={(session) => adoptSession(session.token, session.user)}
          onCancel={() => setPending(null)}
        />
      ) : (
        <>
          <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Sign in</h2>
          <p className="mt-1.5 text-[13px] text-muted">
            {setupRequired
              ? 'This bastion has not been set up yet. Sign in as the administrator to configure it.'
              : forCLI
                ? 'Then approve the kubemg login waiting in your terminal.'
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
          <SsoProviderSlot
            providers={providers}
            onBusyChange={setBusy}
            onChallenge={(challenge, account) => setPending({ challenge, account })}
          />
        </>
      )}
    </AuthLayout>
  )
}
