import { createContext, useContext } from 'react'
import type { ReactNode } from 'react'

/**
 * Asking before something irreversible happens.
 *
 * This existed as `window.confirm` on fourteen call sites — deleting a user, a
 * group, a machine account, a session recording, a template, a Helm repository,
 * revoking a credential. The operating system's box cannot be told what is
 * going, cannot say what else goes with it, cannot name the act in the same word
 * as the button that opened it, and looks like a page trying to trap the reader
 * rather than like this product. Every other write in this console goes through
 * `Sheet`; the question that precedes a write should too.
 *
 * It is a context rather than a component because of how it is *called*. The
 * fourteen sites are all `if (!confirm(…)) return` at the top of an async
 * function, and that shape is worth keeping: a confirmation rewritten as
 * open-state plus a pending-action ref is four new pieces of state per page and
 * a new way for each page to get it wrong. So `useConfirm()` hands back a
 * function returning a promise, the sheet is mounted once above the router, and
 * the call site changes by one `await`.
 */
export interface ConfirmRequest {
  /** The act, in the same word as the control that asked for it: "Delete group". */
  title: string
  /** What is about to happen, what it takes with it, and what cannot be undone. */
  body: ReactNode
  /** The button's word — `Delete`, `Revoke`, `Remove`, `Discard`. Never "OK". */
  confirmLabel: string
  /**
   * `danger` for anything irreversible or outward-facing, which is nearly
   * everything that reaches here. `default` is for a question that only costs
   * the reader their unsaved typing.
   */
  tone?: 'danger' | 'default'
  /** Names what is being acted on, above the title, when the title cannot hold it. */
  eyebrow?: string
}

export type Confirm = (request: ConfirmRequest) => Promise<boolean>

export const ConfirmContext = createContext<Confirm | null>(null)

export function useConfirm(): Confirm {
  const confirm = useContext(ConfirmContext)
  if (!confirm) {
    throw new Error('useConfirm must be used inside <ConfirmProvider>')
  }
  return confirm
}
