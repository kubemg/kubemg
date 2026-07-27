import { useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage } from '../api/client'
import { Button, Field, Notice, TextInput } from '../components/primitives'
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
    <main className="flex min-h-svh flex-col items-center justify-center gap-5 bg-ink p-6">
      <div className="flex items-center gap-2.5">
        <span className="grid size-6 place-items-center rounded-[5px] bg-primary font-mono text-[11px] font-bold text-white">
          MG
        </span>
        <span className="text-[15px] font-bold tracking-[0.14em] text-white">KUBEMG</span>
      </div>

      <form
        onSubmit={handleSubmit}
        className="flex w-full max-w-[340px] flex-col gap-3.5 rounded-panel border border-line bg-surface p-5 lift"
      >
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

        <Button type="submit" variant="primary" disabled={busy} className="mt-0.5 py-2">
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </form>

      <p className="text-[11.5px] text-ink-faint">Central access to your Kubernetes fleet</p>
    </main>
  )
}
