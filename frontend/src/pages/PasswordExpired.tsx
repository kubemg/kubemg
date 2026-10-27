import { useState } from 'react'
import type { FormEvent } from 'react'
import { KeyRound } from 'lucide-react'
import { changeOwnPassword, errorMessage, fetchMe } from '../api/client'
import type { User } from '../api/types'
import { AuthLayout } from '../components/AuthLayout'
import { PasswordFields } from '../components/PasswordFields'
import { EMPTY_PASSWORD_DRAFT, passwordDraftBlocker } from '../lib/passwordDraft'
import { Button, Notice } from '../components/primitives'
import { formatInstant } from '../lib/time'
import { useAuth } from '../state/auth-context'

/**
 * Where a session lands when its password is due — the age policy's window
 * passed, or an administrator expired it. The server holds such a session to
 * reading who it is and changing the password, so the console shows this in
 * place of every page rather than a console whose every read is refused.
 *
 * It is the sign-in's frame (`AuthLayout`), not `AppShell`: until the password
 * is changed the person is still on the way in.
 */
export function PasswordExpired() {
  const { user, signOut } = useAuth()
  return (
    <AuthLayout
      cardTestId="password-expired-card"
      headline={
        <>
          Your password
          <br />
          has to change.
        </>
      }
      lede="Nothing else in the console opens until it does. Kubeconfigs and kubemg command-line sessions you already hold keep working."
      footnote="Choose a password you have not used here before."
    >
      <PasswordExpiredBody user={user} onSignOut={signOut} />
    </AuthLayout>
  )
}

/** The card without its frame, so its decisions are testable alone. */
export function PasswordExpiredBody({ user, onSignOut }: { user: User | null; onSignOut: () => void }) {
  const { replaceUser } = useAuth()
  const [draft, setDraft] = useState(EMPTY_PASSWORD_DRAFT)
  const [tried, setTried] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    setTried(true)
    const missing = passwordDraftBlocker(draft)
    if (missing) {
      document.getElementById(missing)?.focus()
      return
    }
    setBusy(true)
    setError(null)
    try {
      await changeOwnPassword({ current_password: draft.current, new_password: draft.next })
      // The account read back no longer carries the flag, and that is what
      // lifts this screen.
      replaceUser(await fetchMe())
    } catch (err) {
      setError(errorMessage(err, 'Could not change the password.'))
      setBusy(false)
    }
  }

  const due = user?.password_expires_at
  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex items-center gap-2 text-muted">
        <KeyRound aria-hidden="true" className="size-4" />
        <span className="text-[12.5px] font-medium">Password expired</span>
      </div>
      <h2 className="mt-2 text-[22px] font-bold tracking-[-0.02em] text-fg">Choose a new password</h2>
      <p className="mt-1.5 text-[13px] text-muted">
        {due ? (
          <>
            The password for <span className="font-data text-fg">{user?.username}</span> expired on{' '}
            {formatInstant(due)}.
          </>
        ) : (
          <>
            The password for <span className="font-data text-fg">{user?.username ?? 'this account'}</span> has
            expired.
          </>
        )}
      </p>

      <form onSubmit={submit} className="mt-6 flex flex-col gap-4" noValidate>
        {error ? <Notice tone="error">{error}</Notice> : null}
        <PasswordFields draft={draft} onChange={setDraft} tried={tried} />
        <Button type="submit" variant="primary" disabled={busy} className="h-10 w-full">
          {busy ? 'Changing…' : 'Change password and continue'}
        </Button>
      </form>

      <button
        type="button"
        onClick={onSignOut}
        className="mt-4 self-start text-[12.5px] text-muted transition-colors hover:text-fg hover:underline"
      >
        Sign out instead
      </button>
    </div>
  )
}
