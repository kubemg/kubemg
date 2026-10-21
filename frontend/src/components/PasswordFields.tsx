import type { PasswordDraft } from '../lib/passwordDraft'
import { Field, TextInput } from './primitives'

/**
 * The three fields of a password change — current, new, and the new one again
 * — shared by the sheet on the profile and the screen a due password lands on,
 * so the two cannot drift.
 *
 * The current password is asked for because the server requires it: a live
 * session must not be enough to lock its owner out of their own account. The
 * confirmation is this side's only extra rule, and it is not a policy — it
 * catches a typo in a value nobody can read back.
 */
export function PasswordFields({
  draft,
  onChange,
  tried,
}: {
  draft: PasswordDraft
  onChange: (next: PasswordDraft) => void
  /** Set by the first submit: from then an empty field says so under itself. */
  tried: boolean
}) {
  const mismatch = draft.confirm !== '' && draft.confirm !== draft.next
  return (
    <>
      <Field
        label="Current password"
        htmlFor="password-current"
        error={tried && !draft.current ? 'Enter the password you signed in with.' : undefined}
      >
        <TextInput
          id="password-current"
          type="password"
          autoComplete="current-password"
          value={draft.current}
          onChange={(event) => onChange({ ...draft, current: event.target.value })}
        />
      </Field>
      <Field
        label="New password"
        htmlFor="password-new"
        hint="At least 8 characters — the rule your account was created under."
        error={tried && !draft.next ? 'Choose the new password.' : undefined}
      >
        <TextInput
          id="password-new"
          type="password"
          autoComplete="new-password"
          value={draft.next}
          onChange={(event) => onChange({ ...draft, next: event.target.value })}
        />
      </Field>
      <Field
        label="Confirm the new password"
        htmlFor="password-confirm"
        error={
          mismatch
            ? 'The two do not match.'
            : tried && !draft.confirm
              ? 'Type the new password again.'
              : undefined
        }
      >
        <TextInput
          id="password-confirm"
          type="password"
          autoComplete="new-password"
          value={draft.confirm}
          onChange={(event) => onChange({ ...draft, confirm: event.target.value })}
        />
      </Field>
    </>
  )
}
