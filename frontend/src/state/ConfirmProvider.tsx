import { useCallback, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

import { Button, Sheet } from '../components/primitives'
import { ConfirmContext } from './confirm-context'
import type { Confirm, ConfirmRequest } from './confirm-context'

/**
 * ConfirmProvider holds the one confirmation sheet and the promise it settles.
 *
 * Exactly one question is ever open, for the same reason exactly one `Sheet` is:
 * a confirmation is asked in answer to a click, and a click cannot land while
 * one is on screen. So this is a single slot rather than a queue — a second
 * request while one is open would be a bug, and it settles the first as `false`
 * rather than silently dropping either.
 *
 * Every path out of the sheet settles the promise: the button, the cancel, the
 * close control, Escape and the scrim (both of which `Sheet` routes to
 * `onClose`). A promise left unsettled would leave the caller's `await` hanging
 * for the life of the page, which is the one failure mode a confirmation must
 * not have.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [request, setRequest] = useState<ConfirmRequest | null>(null)
  const settle = useRef<((answer: boolean) => void) | null>(null)

  const answer = useCallback((value: boolean) => {
    const resolve = settle.current
    settle.current = null
    setRequest(null)
    resolve?.(value)
  }, [])

  const confirm = useCallback<Confirm>(
    (next) =>
      new Promise<boolean>((resolve) => {
        // A question already on screen is refused rather than replaced.
        settle.current?.(false)
        settle.current = resolve
        setRequest(next)
      }),
    [],
  )

  const value = useMemo(() => confirm, [confirm])

  return (
    <ConfirmContext.Provider value={value}>
      {children}
      {request ? (
        <Sheet
          title={request.title}
          eyebrow={request.eyebrow}
          width="md"
          onClose={() => answer(false)}
          footer={
            <>
              <Button type="button" variant="ghost" onClick={() => answer(false)}>
                Cancel
              </Button>
              {/* The act's own word, so the button says what the reader clicked
                  to get here rather than agreeing to something unnamed. */}
              <Button
                type="button"
                variant={request.tone === 'default' ? 'primary' : 'danger'}
                autoFocus
                onClick={() => answer(true)}
              >
                {request.confirmLabel}
              </Button>
            </>
          }
        >
          <div className="text-[13px] leading-relaxed text-muted">{request.body}</div>
        </Sheet>
      ) : null}
    </ConfirmContext.Provider>
  )
}
