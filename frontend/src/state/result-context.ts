import { createContext, useContext } from 'react'
import type { ReactNode } from 'react'

/**
 * What just happened.
 *
 * Nothing in this console said so. Deleting a pod, rolling a release back,
 * revoking a credential — each screen reported its own result its own way, or
 * did not report one at all and simply reloaded. For a product whose whole
 * proposition is that a destructive act is attributable, the sentence confirming
 * that the act landed is the most reassuring surface it has, and it did not
 * exist.
 *
 * Three rules, and they are what keep this from becoming a toast library:
 *
 *  - **It uses the control's own word.** `Uninstall` produces `Uninstalled`,
 *    `Revoke` produces `Revoked`. A result that renames the act makes the reader
 *    check whether the thing they clicked is the thing that happened.
 *  - **A failure says what to do next**, not only that something failed. The
 *    error the server wrote is the body; the title is this console's own voice.
 *  - **It links to the record it produced.** Every act here lands in the audit
 *    trail, and the result is the one moment the reader is holding the context
 *    needed to find that row.
 *
 * And it is quiet: no slide, no fade, no motion of any kind, on a deck whose
 * rule is that nothing moves. It appears, it is read, and a success takes itself
 * away after a while. A failure never does — it waits to be dismissed, because
 * the one result nobody may miss is the one that says the act did not land.
 */
export interface ResultReport {
  /** `ok` for an act that landed, `warn` for one that landed partly, `error` for one that did not. */
  tone: 'ok' | 'warn' | 'error'
  /** The act in its past tense, in the control's own word: "Uninstalled". */
  title: string
  /** What happened, or — on a failure — what to do about it. */
  body?: ReactNode
  /** Where the record of this act can be read. Usually the audit trail, filtered. */
  link?: { to: string; label: string }
}

export type Report = (result: ResultReport) => void

export const ResultContext = createContext<Report | null>(null)

export function useResult(): Report {
  const report = useContext(ResultContext)
  if (!report) {
    throw new Error('useResult must be used inside <ResultProvider>')
  }
  return report
}
