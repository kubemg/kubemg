import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight } from 'lucide-react'
import { errorMessage } from '../api/client'
import { Button, Field, Notice, TextInput } from '../components/primitives'
import { LinkStrand } from '../components/LinkStrand'
import { Mark } from '../components/Mark'
import { SsoLoginPage } from '../components/SsoLoginPage'
import { useAuth } from '../state/auth-context'

export function Login() {
  const { signIn } = useAuth()
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
      setError(errorMessage(err, 'Sign in failed.'))
      setBusy(false)
    }
  }

  return (
    <main className="grid min-h-svh lg:grid-cols-[1.1fr_minmax(420px,0.9fr)]">
      {/* The left half is the product in one picture: clusters dial out, and
          everything an operator does travels back along those strands. */}
      <section className="relative hidden flex-col justify-between overflow-hidden bg-rail p-10 lg:flex">
        <div className="flex items-center gap-2.5">
          <Mark className="size-7 shrink-0 text-accent" />
          <span className="text-[16px] font-semibold tracking-[-0.02em]">
            <span className="text-rail-fg">Kube</span>
            <span className="text-accent">MG</span>
          </span>
        </div>

        <div className="max-w-md">
          <h1 className="text-[34px] leading-[1.1] font-semibold tracking-[-0.03em] text-rail-fg">
            Clusters dial out.
            <br />
            Nothing dials in.
          </h1>
          <p className="mt-4 text-[14px] leading-relaxed text-rail-muted">
            Every cluster holds an outbound tunnel to KubeMG. Access is issued here, kubectl traffic
            is proxied under your own identity, and every call lands in the audit trail.
          </p>

          <Convergence />
        </div>

        <p className="font-mono text-[11.5px] text-rail-faint">
          kubemg · centralized Kubernetes access
        </p>
      </section>

      <section className="flex items-center justify-center bg-bg p-6">
        <div className="w-full max-w-[380px]">
          <div className="mb-7 lg:hidden">
            <span className="text-[20px] font-semibold tracking-[-0.02em]">
              <span className="text-fg">Kube</span>
              <span className="text-accent">MG</span>
            </span>
          </div>

          <h2 className="text-[22px] font-semibold tracking-[-0.02em] text-fg">Sign in</h2>
          <p className="mt-1.5 text-[13px] text-muted">
            Use the account your administrator issued.
          </p>

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

          {/* Federated sign-in, when an administrator has configured any. It
              renders nothing at all otherwise, so a single-tenant install never
              sees a divider over an empty space. */}
          <SsoLoginPage onBusyChange={setBusy} />
        </div>
      </section>
    </main>
  )
}

/**
 * Convergence draws the shape of the product: several clusters, one node. The
 * strands are the same device used throughout the console, so the login page
 * teaches the reading before anyone needs it.
 */
function Convergence() {
  return (
    <div className="mt-10 flex items-center gap-4" aria-hidden="true">
      <div className="flex flex-1 flex-col gap-3">
        {(['live', 'live', 'idle'] as const).map((state, index) => (
          <div key={index} className="flex items-center gap-3">
            <span className="size-1.5 shrink-0 rounded-full bg-rail-faint" />
            <LinkStrand state={state} className="flex-1" />
          </div>
        ))}
      </div>
      <span className="grid size-11 shrink-0 place-items-center rounded-card border border-accent-line bg-accent-soft font-mono text-[12px] font-semibold text-accent">
        MG
      </span>
    </div>
  )
}
