import { useState } from 'react'
import { errorMessage, updateOwnProfile } from '../api/client'
import type { User } from '../api/types'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/**
 * Editing your own account details.
 *
 * Only the email. The username is the identity the gateway impersonates and the
 * name every audit record carries, so renaming it stays an administrator's act —
 * the sheet says so rather than drawing a field that would be refused. Offered
 * only for an account whose details live here: a federated account's are its
 * directory's and are written back at every sign-in.
 */
export function ProfileSheet({ user, onClose, onSaved }: {
  user: User
  onClose: () => void
  onSaved: (user: User) => void
}) {
  const [email, setEmail] = useState(user.email ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const unchanged = email.trim() === (user.email ?? '')

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || unchanged) return
    setBusy(true)
    setError(null)
    try {
      onSaved(await updateOwnProfile({ email: email.trim() }))
    } catch (err) {
      setError(errorMessage(err, 'Could not update the profile.'))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="Edit your profile"
      eyebrow="Your account"
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy || unchanged}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      <Field
        label="Username"
        htmlFor="profile-username"
        hint="The name the clusters see you as and every audit record carries — only an administrator can change it."
      >
        <TextInput id="profile-username" value={user.username} disabled readOnly className="font-data" />
      </Field>
      <Field
        label="Email"
        htmlFor="profile-email"
        hint="Optional. Leave it empty to remove the address."
      >
        <TextInput
          id="profile-email"
          type="email"
          autoComplete="email"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          className="font-data"
        />
      </Field>
    </Sheet>
  )
}
