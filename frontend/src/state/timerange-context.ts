import { createContext, useContext } from 'react'
import { DEFAULT_TIME_RANGE } from '../lib/timerange'
import type { TimeRangeId } from '../lib/timerange'

export interface TimeRangeState {
  /** The window every ranged surface on the page reads. */
  range: TimeRangeId
  setRange: (next: TimeRangeId) => void
}

export const TimeRangeContext = createContext<TimeRangeState | null>(null)

/**
 * The console's window. Unlike the other contexts this one has a working
 * default rather than throwing when it is missing: a chart rendered outside the
 * shell — in a test, or in a surface that has not been moved yet — should draw
 * its default hour instead of crashing the page it is on.
 */
export function useTimeRange(): TimeRangeState {
  return (
    useContext(TimeRangeContext) ?? {
      range: DEFAULT_TIME_RANGE,
      setRange: () => {},
    }
  )
}
