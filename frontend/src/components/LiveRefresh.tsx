import { Pause, Radio, RefreshCw } from 'lucide-react'
import { LIVE_INTERVAL, attentive, useAttention, useLive } from '../lib/live'
import { formatClock } from '../lib/time'
import { Button, Chip } from './primitives'

/**
 * Whether the page is keeping itself current, and the switch for it.
 *
 * It is a **word**, not a spinner. A live tick is meant to be invisible (see
 * `lib/query.ts`), and something that animated every fifteen seconds to announce
 * a read nobody asked for would be the loudest thing on a deck whose chrome is
 * meant to be the quietest. What it does say is which of the four states the
 * page is in, because "not updating right now" has three different causes and
 * they call for three different responses:
 *
 *   - **Live** — re-reading on its cadence.
 *   - **Idle** — this tab has not been touched for a while, so it stopped. Move
 *     the mouse and it is live again; that is the whole recovery, which is why it
 *     is worth saying rather than sitting there looking live.
 *   - **Retrying** — a tick failed. What is drawn is the last answer that
 *     landed, deliberately kept, and the next tick says whether it was real.
 *   - **Paused** — somebody turned it off, for the whole console at once.
 */
export function LiveChip({
  stale = false,
  updatedAt = null,
  interval = LIVE_INTERVAL,
}: {
  stale?: boolean
  updatedAt?: number | null
  interval?: number
}) {
  const { live, setLive } = useLive()
  // Subscribed so the word follows the tab going quiet; the tick itself asks
  // `attentive()` directly rather than trusting a render.
  useAttention()

  const state = !live ? 'Paused' : stale ? 'Retrying' : attentive() ? 'Live' : 'Idle'
  const read =
    updatedAt === null ? '' : `Last read at ${formatClock(updatedAt, { seconds: true })}. `

  return (
    <Chip
      active={live}
      onClick={() => setLive(!live)}
      title={
        live
          ? `${read}Re-reads every ${Math.round(interval / 1000)}s while this tab is in front — a hidden or untouched tab reads nothing. Click to pause everywhere.`
          : `${read}Automatic re-reads are paused for the whole console. Refresh still asks the cluster.`
      }
    >
      {live ? (
        <Radio aria-hidden="true" className="size-3.5" />
      ) : (
        <Pause aria-hidden="true" className="size-3.5" />
      )}
      {state}
    </Chip>
  )
}

/**
 * The header control for a page that reads one thing and keeps it current.
 *
 * State and escape hatch sit together because there is one question behind
 * both — *is what I am reading now?* — and neither half answers it alone: the
 * chip says whether the page re-reads on its own, and the button asks the
 * cluster this instant.
 */
export function LiveRefresh({
  query,
  disabled = false,
  interval = LIVE_INTERVAL,
}: {
  query: {
    loading: boolean
    revalidating: boolean
    stale: boolean
    updatedAt: number | null
    refresh: () => Promise<void>
  }
  disabled?: boolean
  interval?: number
}) {
  // A live tick is deliberately absent from this: the button must not disable
  // itself, or spin, four times a minute for a read nobody asked for.
  const busy = query.loading || query.revalidating

  return (
    <div className="flex items-center gap-2">
      <LiveChip stale={query.stale} updatedAt={query.updatedAt} interval={interval} />
      <Button onClick={() => void query.refresh()} disabled={disabled || busy}>
        <RefreshCw aria-hidden="true" className={`size-4 ${busy ? 'animate-spin' : ''}`} />
        Refresh
      </Button>
    </div>
  )
}
