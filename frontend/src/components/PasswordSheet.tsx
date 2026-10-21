import { useState } from 'react'
import { changeOwnPassword, errorMessage } from '../api/client'
import type { PasswordChangeResult } from '../api/types'
import { Button, Notice, Sheet } from './primitives'
import { EMPTY_PASSWORD_DRAFT, passwordDraftBlocker } from '../lib/passwordDraft'
import { PasswordFields } from './PasswordFields'

/**
 * Changing your own password.
 *
 * It is a sheet on the credentials page rather than a page of its own because
 * it is one act on the account the page is already about, and because the thing
 * it optionally does — taking the issued kubeconfigs with the rotation — is the
 * register sitting behind it. Somebody rotating a password because they think
 * it leaked wants both; somebody rotating one on a schedule wants only the
 * first, which is why the revoke is a choice here and never silent. The three
 * fields are `PasswordFields`, shared with the screen a due password lands on.
 */
export function PasswordSheet({ onClose, onRevoked }: {
  onClose: () => void
  /** Called when the rotation also revoked, so the register behind reloads. */
  onRevoked?: (result: PasswordChangeResult) => void
}) {
  const [draft, setDraft] = useState(EMPTY_PASSWORD_DRAFT)
  const [revoke, setRevoke] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<PasswordChangeResult | null>(null)

  // Set by the first submit: from then an empty field says so under itself.
  const [tried, setTried] = useState(false)

  async function submit(event: React.FormEvent<HTMLFormElement>) {
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
      const result = await changeOwnPassword({
        current_password: draft.current,
        new_password: draft.next,
        revoke_kubeconfigs: revoke,
      })
      setDone(result)
      setDraft(EMPTY_PASSWORD_DRAFT)
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
              disabled={busy}
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
              {done.credentials.cli_sessions_revoked
                ? ` ${done.credentials.cli_sessions_revoked} signed-in kubemg ${done.credentials.cli_sessions_revoked === 1 ? 'command line was' : 'command lines were'} ended too.`
                : null}
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
          <PasswordFields draft={draft} onChange={setDraft} tried={tried} />

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
