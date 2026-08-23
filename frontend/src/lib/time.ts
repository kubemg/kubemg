import { useEffect, useState } from 'react'

/** secondsUntil returns whole seconds left before an ISO timestamp, never negative. */
export function secondsUntil(iso: string): number {
  const remaining = Math.floor((new Date(iso).getTime() - Date.now()) / 1000)
  return Number.isFinite(remaining) && remaining > 0 ? remaining : 0
}

/**
 * formatDuration renders seconds as hh:mm:ss, the way a countdown should read —
 * except past two days, where a clock face stops being a countdown. "2160:00:00"
 * is a number nobody reads as three months, so a long window reads as days and
 * hours instead. The cut is at 48h because "47:12:03" is still a shift somebody
 * is counting down; "72:00:00" is not.
 */
export function formatDuration(totalSeconds: number): string {
  if (totalSeconds >= 48 * 3600) {
    const days = Math.floor(totalSeconds / 86400)
    const hours = Math.floor((totalSeconds % 86400) / 3600)
    return `${days}d ${String(hours).padStart(2, '0')}h`
  }
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  return [hours, minutes, seconds].map((part) => String(part).padStart(2, '0')).join(':')
}

/**
 * formatTTL renders a window somebody is *choosing* rather than watching run
 * out: "8 hours", "30 days". formatWindow below does the same job for minutes
 * in operator shorthand; this one is spelled out because it labels a control
 * whose options have to be told apart at a glance, and "90d" beside "30d" is
 * two characters of difference.
 */
export function formatTTL(seconds: number): string {
  if (seconds % 86400 === 0) {
    const days = seconds / 86400
    return days === 1 ? '1 day' : `${days} days`
  }
  if (seconds % 3600 === 0) {
    const hours = seconds / 3600
    return hours === 1 ? '1 hour' : `${hours} hours`
  }
  const minutes = Math.round(seconds / 60)
  return minutes === 1 ? '1 minute' : `${minutes} minutes`
}

/**
 * formatWindow renders a number of minutes the way somebody asks for one — "30m",
 * "4h", "1h 30m" — as opposed to formatDuration above, which renders a clock
 * running out. A window is chosen and a countdown is watched, and they read
 * differently for that reason.
 */
export function formatWindow(minutes: number): string {
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest === 0 ? `${hours}h` : `${hours}h ${rest}m`
}

/** relativeAge renders how long ago something happened, in operator shorthand. */
export function relativeAge(iso: string | undefined): string {
  if (!iso) return 'never'

  const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (!Number.isFinite(seconds)) return 'never'
  if (seconds < 45) return 'just now'
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`
  return `${Math.round(seconds / 86400)}d ago`
}

/**
 * useCountdown ticks once a second toward an expiry. Credentials issued here are
 * deliberately short-lived, so the UI shows them running out.
 */
export function useCountdown(expiresAt: string | null): number {
  const [remaining, setRemaining] = useState(() => (expiresAt ? secondsUntil(expiresAt) : 0))

  useEffect(() => {
    if (!expiresAt) {
      setRemaining(0)
      return
    }

    setRemaining(secondsUntil(expiresAt))
    const timer = window.setInterval(() => {
      const next = secondsUntil(expiresAt)
      setRemaining(next)
      if (next === 0) window.clearInterval(timer)
    }, 1000)

    return () => window.clearInterval(timer)
  }, [expiresAt])

  return remaining
}

/**
 * formatCountdown renders time until something the cluster will do on its own,
 * as opposed to formatDuration's clock face: a schedule is read down a column of
 * rows, so it has to be scannable rather than precise. Seconds are only shown
 * where they are what somebody is watching — under ten minutes — because "in 6d
 * 04h" and "in 6d 04h 12m 09s" answer the same question and only one of them
 * fits in a cell.
 */
export function formatCountdown(totalSeconds: number): string {
  if (totalSeconds <= 0) return 'due now'
  if (totalSeconds < 60) return `in ${totalSeconds}s`
  if (totalSeconds < 600) return `in ${Math.floor(totalSeconds / 60)}m ${totalSeconds % 60}s`
  if (totalSeconds < 3600) return `in ${Math.floor(totalSeconds / 60)}m`
  if (totalSeconds < 86400) {
    return `in ${Math.floor(totalSeconds / 3600)}h ${Math.floor((totalSeconds % 3600) / 60)}m`
  }
  return `in ${Math.floor(totalSeconds / 86400)}d ${Math.floor((totalSeconds % 86400) / 3600)}h`
}

/**
 * useTicker re-renders on a cadence so a list of countdowns stays true, with one
 * timer for the whole list rather than useCountdown's one per value — a table of
 * fifty schedules would otherwise hold fifty intervals.
 *
 * The cadence is the caller's, because it depends on what is being counted: a
 * schedule firing in six days does not need a second-by-second redraw, and this
 * deck's rule is that nothing moves without a reason.
 */
export function useTicker(intervalMs: number): void {
  const [, setTick] = useState(0)

  useEffect(() => {
    const timer = window.setInterval(() => setTick((tick) => tick + 1), intervalMs)
    return () => window.clearInterval(timer)
  }, [intervalMs])
}
