import { useEffect, useState } from 'react'

/** secondsUntil returns whole seconds left before an ISO timestamp, never negative. */
export function secondsUntil(iso: string): number {
  const remaining = Math.floor((new Date(iso).getTime() - Date.now()) / 1000)
  return Number.isFinite(remaining) && remaining > 0 ? remaining : 0
}

/** formatDuration renders seconds as hh:mm:ss, the way a countdown should read. */
export function formatDuration(totalSeconds: number): string {
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  return [hours, minutes, seconds].map((part) => String(part).padStart(2, '0')).join(':')
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
