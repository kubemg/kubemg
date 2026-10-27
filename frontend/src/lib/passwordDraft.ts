/** What the three fields of a password change hold; see PasswordFields. */
export interface PasswordDraft {
  current: string
  next: string
  confirm: string
}

export const EMPTY_PASSWORD_DRAFT: PasswordDraft = { current: '', next: '', confirm: '' }

/** The id of the first field that stops a submit, or null when it may go. */
export function passwordDraftBlocker(draft: PasswordDraft): string | null {
  if (!draft.current) return 'password-current'
  if (!draft.next) return 'password-new'
  if (!draft.confirm || draft.confirm !== draft.next) return 'password-confirm'
  return null
}
