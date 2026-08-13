import { useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Moon, Sun } from 'lucide-react'
import { errorMessage } from '../api/client'
import { Button, Field, Notice, TextInput } from '../components/primitives'
import { Mark } from '../components/Mark'
import { SsoLoginPage } from '../components/SsoLoginPage'
import { useAuth } from '../state/auth-context'
import { useTheme } from '../lib/theme'

export function Login() {
  const { signIn } = useAuth()
  const { theme, toggle } = useTheme()
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
      <button
        type="button"
        onClick={toggle}
        title={theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
        className="fixed top-4 right-4 z-10 grid size-9 place-items-center rounded-control border border-line bg-surface text-muted transition-colors hover:bg-raised hover:text-fg"
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

      {/* The left half is the product's own words: clusters dial out, and
          nothing here needs to prove that with a diagram. */}
      <section className="relative hidden flex-col justify-between overflow-hidden bg-rail p-10 lg:flex">
        {/* Texture, not signal — a static field standing in for the fleet, and
            a soft accent glow anchoring the corner. Nothing here animates: the
            link strand is the deck's one moving part, and this page doesn't
            draw one. */}
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
            backgroundImage: 'radial-gradient(circle, var(--deck-accent-soft), transparent 70%)',
          }}
        />

        <div className="relative flex items-center gap-2.5">
          <Mark className="size-7 shrink-0 text-accent" />
          <span className="text-[16px] font-semibold tracking-[-0.02em]">
            <span className="text-rail-fg">Kube</span>
            <span className="text-accent">MG</span>
          </span>
        </div>

        <div className="relative max-w-md">
          <h1 className="text-[34px] leading-[1.1] font-semibold tracking-[-0.03em] text-rail-fg">
            Clusters dial out.
            <br />
            Nothing dials in.
          </h1>
          <p className="mt-4 text-[14px] leading-relaxed text-rail-muted">
            Every cluster holds an outbound tunnel to KubeMG. Access is issued here, kubectl traffic
            is proxied under your own identity, and every call lands in the audit trail.
          </p>
        </div>

        <p className="relative font-mono text-[11.5px] text-rail-faint">
          kubemg · centralized Kubernetes access
        </p>
      </section>

      <section className="flex items-center justify-center bg-bg p-6">
        <div className="card lift w-full max-w-[380px] p-8">
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
