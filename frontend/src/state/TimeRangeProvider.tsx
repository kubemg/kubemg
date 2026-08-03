import { useCallback, useMemo } from 'react'
import type { ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { DEFAULT_TIME_RANGE, TIME_RANGE_PARAM, isTimeRange } from '../lib/timerange'
import type { TimeRangeId } from '../lib/timerange'
import { TimeRangeContext } from './timerange-context'
import type { TimeRangeState } from './timerange-context'

/**
 * The window lives in the address, not in state.
 *
 * A console window is a thing people send each other — "look at prod between
 * these two" is a link, and a link that drops the range it was read at sends
 * whoever opens it to a different picture under the same URL. Holding it in the
 * query string means the browser's own back button walks the windows too.
 *
 * It is written with `replace` because changing the range is refining a view
 * rather than navigating: pushing an entry per preset would make Back mean
 * "step through every window I tried" instead of "the page I came from".
 *
 * An unrecognised value falls back to the default rather than being refused —
 * the vocabulary is closed on the server, and a stale bookmark naming a preset
 * this build no longer offers should open on something rather than nothing.
 */
export function TimeRangeProvider({ children }: { children: ReactNode }) {
  const [params, setParams] = useSearchParams()
  const raw = params.get(TIME_RANGE_PARAM)
  const range = isTimeRange(raw) ? raw : DEFAULT_TIME_RANGE

  const setRange = useCallback(
    (next: TimeRangeId) => {
      setParams(
        (current) => {
          const updated = new URLSearchParams(current)
          updated.set(TIME_RANGE_PARAM, next)
          return updated
        },
        { replace: true },
      )
    },
    [setParams],
  )

  const value = useMemo<TimeRangeState>(() => ({ range, setRange }), [range, setRange])

  return <TimeRangeContext.Provider value={value}>{children}</TimeRangeContext.Provider>
}
