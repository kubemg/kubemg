import { useState } from 'react'
import { changeOwnPassword, errorMessage } from '../api/client'
import type { PasswordChangeResult } from '../api/types'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/**
 * Changing your own password.
 *
 * It is a sheet on the credentials page rather than a page of its own because
 * it is one act on the account the page is already about, and because the thing
 * it optionally does — taking the issued kubeconfigs with the rotation — is the
 * register sitting behind it. Somebody rotating a password because they think
 * it leaked wants both; somebody rotating one on a schedule wants only the
 * first, which is why the revoke is a choice here and never silent.
 *
 * The current password is asked for because the server requires it: a live
 * session must not be enough to lock its owner out of their own account. The
 * confirmation field is this side's only extra rule, and it is not a policy —
 * it catches a typo in a value nobody can read back.
 */
export function PasswordSheet({ onClose, onRevoked }: {
  onClose: () => void
  /** Called when the rotation also revoked, so the register behind reloads. */
  onRevoked?: (result: PasswordChangeResult) => void
}) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [revoke, setRevoke] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<PasswordChangeResult | null>(null)

  const mismatch = confirm !== '' && confirm !== next

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (mismatch || busy) return
    setBusy(true)
    setError(null)
    try {
      const result = await changeOwnPassword({
        current_password: current,
        new_password: next,
        revoke_kubeconfigs: revoke,
      })
      setDone(result)
      setCurrent('')
      setNext('')
      setConfirm('')
      if (result.credentials) onRevoked?.(result)
    } catch (err) {
      setError(errorMessage(err, 'Could not change the password.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="Change your password"
      eyebrow="Your account"
      onClose={onClose}
      onSubmit={submit}
      footer={
        done ? (
          <Button type="button" onClick={onClose}>
            Close
          </Button>
        ) : (
          <>
            <Button type="button" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={busy || mismatch || !current || !next || !confirm}
            >
              {busy ? 'Changing…' : 'Change password'}
            </Button>
          </>
        )
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      {done ? (
        <Notice tone="ok">
          Your password is changed. Sessions already signed in keep working — the password is what
          opens a new one.
          {done.credentials ? (
            <>
              {' '}
              {done.credentials.revoked}{' '}
              {done.credentials.revoked === 1 ? 'kubeconfig was' : 'kubeconfigs were'} revoked with
              it.
              {done.credentials.still_valid > 0 ? (
                <>
                  {' '}
                  {done.credentials.still_valid} still{' '}
                  {done.credentials.still_valid === 1 ? 'works' : 'work'} on{' '}
                  {(done.credentials.clusters_not_reached ?? []).join(', ')}.{' '}
                  {done.credentials.explanation}
                </>
              ) : null}
            </>
          ) : null}
        </Notice>
      ) : (
        <>
          <Field label="Current password" htmlFor="password-current">
            <TextInput
              id="password-current"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(event) => setCurrent(event.target.value)}
            />
          </Field>
          <Field
            label="New password"
            htmlFor="password-new"
            hint="At least 8 characters — the rule your account was created under."
          >
            <TextInput
              id="password-new"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(event) => setNext(event.target.value)}
            />
          </Field>
          <Field
            label="Confirm the new password"
            htmlFor="password-confirm"
            error={mismatch ? 'The two do not match.' : undefined}
          >
            <TextInput
              id="password-confirm"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(event) => setConfirm(event.target.value)}
            />
          </Field>

          <label htmlFor="password-revoke" className="flex cursor-pointer items-start gap-3">
            <input
              id="password-revoke"
              type="checkbox"
              checked={revoke}
              onChange={(event) => setRevoke(event.target.checked)}
              className="mt-0.5 size-4 shrink-0 accent-[var(--color-accent)]"
            />
            <span className="min-w-0">
              <span className="block text-[13.5px] text-fg">
                Revoke my issued kubeconfigs as well
              </span>
              <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                For a password you think has leaked. Anything using one of those files starts
                failing at its next call; a kubeconfig for a cluster registered for direct API
                access cannot be withdrawn from here and will be named in the result.
              </span>
            </span>
          </label>
        </>
      )}
    </Sheet>
  )
}
