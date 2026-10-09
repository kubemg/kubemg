import type { ManagedBy } from '../api/types'
import { type ManagedAct, managedNotice, managedTone } from '../lib/managedBy'
import { Notice } from './primitives'

/**
 * The notice a write surface shows when something else reconciles the object
 * it is about to change — see `lib/managedBy.ts`. Absent, not empty, when
 * nothing does or the act is one its manager leaves alone.
 */
export function ManagedByNotice({
  managed,
  label,
  act,
}: {
  managed?: ManagedBy
  /** The singular Kind, for "This Deployment …". */
  label: string
  act: ManagedAct
}) {
  const text = managedNotice(managed, label, act)
  if (!managed || !text) return null
  return <Notice tone={managedTone(managed)}>{text}</Notice>
}
