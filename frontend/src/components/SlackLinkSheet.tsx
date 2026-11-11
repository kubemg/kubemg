import { useState } from 'react'
import { errorMessage, updateUser } from '../api/client'
import type { User } from '../api/types'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/**
 * Linking a person's Slack member ID, so their Approve and Reject clicks on an
 * access-request message count as this account's.
 *
 * The member ID and not the handle: a handle is a display name its owner may be
 * able to change, so matching one against a username would let anyone in the
 * workspace decide as whoever they renamed themselves after. An unlinked click
 * decides nothing.
 */
export function SlackLinkSheet({ user, onClose, onSaved }: {
  user: User
  onClose: () => void
  onSaved: (user: User) => void
}) {
  const [memberID, setMemberID] = useState(user.slack_user_id ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const unchanged = memberID.trim().toUpperCase() === (user.slack_user_id ?? '')

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || unchanged) return
    setBusy(true)
    setError(null)
    try {
      onSaved(await updateUser(user.id, { slack_user_id: memberID.trim() }))
    } catch (err) {
      setError(errorMessage(err, 'The Slack link was not saved.'))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="Link a Slack account"
      eyebrow={user.username}
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
      <p className="text-[13px] text-muted">
        Approve and Reject on an access-request message in Slack count as this account only when the
        person who clicks is linked here. Find the member ID in their Slack profile, under the
        three-dot menu: <span className="font-data">Copy member ID</span>.
      </p>
      <Field
        label="Slack member ID"
        htmlFor="slack-member-id"
        hint="Starts with U, or W on Enterprise Grid. Leave it empty to unlink."
      >
        <TextInput
          id="slack-member-id"
          value={memberID}
          onChange={(event) => setMemberID(event.target.value)}
          placeholder="U0123ABCD"
          autoComplete="off"
          spellCheck={false}
          className="font-data"
        />
      </Field>
    </Sheet>
  )
}
