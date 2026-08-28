import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { AlertTriangle, Check, X, XCircle } from 'lucide-react'

import { IconButton } from '../components/primitives'
import { ResultContext } from './result-context'
import type { Report, ResultReport } from './result-context'

/** How long a result that landed stays up. A failure stays until it is dismissed. */
const SETTLE_MS = 9000

/** At most this many at once: past three, the oldest is dropped rather than stacked. */
const MAX_VISIBLE = 3

interface Entry extends ResultReport {
  id: number
}

const TONE: Record<ResultReport['tone'], { plate: string; icon: ReactNode }> = {
  ok: {
    plate: 'border-ok/35 bg-ok-soft text-ok',
    icon: <Check aria-hidden="true" className="size-4" />,
  },
  warn: {
    plate: 'border-warn/35 bg-warn-soft text-warn',
    icon: <AlertTriangle aria-hidden="true" className="size-4" />,
  },
  error: {
    plate: 'border-danger/35 bg-danger-soft text-danger',
    icon: <XCircle aria-hidden="true" className="size-4" />,
  },
}

/**
 * ResultProvider mounts the one place results are said, above the router so a
 * result survives the navigation the act itself often causes — deleting the
 * object a page is about sends the reader back to the list, and the sentence
 * saying it worked must not be torn down on the way.
 *
 * It sits over the page and under a `Sheet` (z-30, beside the shell dock): a
 * result is about the thing behind it, never a surface anybody works in.
 */
export function ResultProvider({ children }: { children: ReactNode }) {
  const [entries, setEntries] = useState<Entry[]>([])
  const nextId = useRef(1)
  const timers = useRef<number[]>([])

  const dismiss = useCallback((id: number) => {
    setEntries((current) => current.filter((entry) => entry.id !== id))
  }, [])

  const report = useCallback<Report>(
    (result) => {
      const id = nextId.current++
      setEntries((current) => [...current, { ...result, id }].slice(-MAX_VISIBLE))
      // Only a result that landed takes itself away. An error waits: the reader
      // may have looked away exactly because they assumed it worked.
      if (result.tone === 'ok') {
        const timer = window.setTimeout(() => dismiss(id), SETTLE_MS)
        timers.current.push(timer)
      }
    },
    [dismiss],
  )

  useEffect(() => {
    const pending = timers.current
    return () => pending.forEach((timer) => window.clearTimeout(timer))
  }, [])

  return (
    <ResultContext.Provider value={report}>
      {children}
      {entries.length > 0 ? (
        <div
          // `polite` rather than `assertive`: a result is worth hearing at the
          // next pause, never worth interrupting what is being read.
          aria-live="polite"
          className="pointer-events-none fixed right-4 bottom-4 z-30 flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2"
        >
          {entries.map((entry) => (
            <div
              key={entry.id}
              className={`pointer-events-auto flex items-start gap-2.5 rounded-card border px-3 py-2.5 lift ${TONE[entry.tone].plate}`}
            >
              <span className="mt-px shrink-0">{TONE[entry.tone].icon}</span>
              <div className="min-w-0 flex-1">
                <p className="text-[13px] font-medium">{entry.title}</p>
                {entry.body ? (
                  <p className="mt-0.5 text-[12.5px] leading-relaxed text-muted">{entry.body}</p>
                ) : null}
                {entry.link ? (
                  <Link
                    to={entry.link.to}
                    onClick={() => dismiss(entry.id)}
                    className="mt-1 inline-block text-[12.5px] text-muted underline underline-offset-2 transition-colors hover:text-fg"
                  >
                    {entry.link.label}
                  </Link>
                ) : null}
              </div>
              <IconButton label="Dismiss" onClick={() => dismiss(entry.id)} type="button">
                <X aria-hidden="true" className="size-3.5" />
              </IconButton>
            </div>
          ))}
        </div>
      ) : null}
    </ResultContext.Provider>
  )
}
