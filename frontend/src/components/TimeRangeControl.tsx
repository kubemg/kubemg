import { TIME_RANGES } from '../lib/timerange'
import type { TimeRangeId } from '../lib/timerange'
import { useTimeRange } from '../state/timerange-context'
import { Select } from './primitives'

/**
 * The console's one window control.
 *
 * It sits in the header rather than on the cards it scopes because it scopes
 * all of them: a chart, the trail beside it and the log search below it are
 * three readings of one span, and a control per card is how they came to
 * disagree. Its value is in the address, so the header is also where the thing
 * a pasted link carries is shown.
 */
export function TimeRangeControl() {
  const { range, setRange } = useTimeRange()

  return (
    <div className="w-36 sm:w-40">
      <Select
        aria-label="Time range"
        size="sm"
        value={range}
        onChange={(event) => setRange(event.target.value as TimeRangeId)}
      >
        {TIME_RANGES.map((entry) => (
          <option key={entry.id} value={entry.id}>
            {entry.label}
          </option>
        ))}
      </Select>
    </div>
  )
}
