/*
 * Reading an asciinema v2 recording.
 *
 * The format is a JSON header line followed by one JSON array per event:
 * [offset, code, data], where the code is "o" for what the container printed,
 * "i" for what the operator typed, and "r" for a window resize. Parsing lives
 * here rather than in the player so the player is only about playback.
 */

export type CastEventCode = 'o' | 'i' | 'r'

export interface CastEvent {
  /** Seconds since the session opened. */
  at: number
  code: CastEventCode
  data: string
}

export interface Cast {
  width: number
  height: number
  /** When the session was recorded, as epoch seconds, when the header carries it. */
  timestamp?: number
  events: CastEvent[]
  /** The offset of the last event: how long the replay runs. */
  duration: number
}

/** The geometry a recording falls back to when its header is unusable. */
const FALLBACK_COLS = 80
const FALLBACK_ROWS = 24

/**
 * parseCast reads a recording. A malformed line is skipped rather than failing
 * the whole replay: a recording of a session that ended badly can legitimately
 * stop mid-line, and showing what was captured up to that point is the point.
 */
export function parseCast(raw: string): Cast {
  const lines = raw.split('\n')
  let width = FALLBACK_COLS
  let height = FALLBACK_ROWS
  let timestamp: number | undefined

  const events: CastEvent[] = []

  for (const [index, line] of lines.entries()) {
    const trimmed = line.trim()
    if (!trimmed) continue

    let parsed: unknown
    try {
      parsed = JSON.parse(trimmed)
    } catch {
      continue
    }

    // The header is the first non-empty line, and it is an object rather than
    // an array — which is also how a truncated file with no header is tolerated.
    if (index === 0 && !Array.isArray(parsed) && typeof parsed === 'object' && parsed !== null) {
      const header = parsed as { width?: number; height?: number; timestamp?: number }
      if (typeof header.width === 'number' && header.width > 0) width = header.width
      if (typeof header.height === 'number' && header.height > 0) height = header.height
      if (typeof header.timestamp === 'number') timestamp = header.timestamp
      continue
    }

    if (!Array.isArray(parsed) || parsed.length < 3) continue
    const [at, code, data] = parsed as [unknown, unknown, unknown]
    if (typeof at !== 'number' || typeof data !== 'string') continue
    if (code !== 'o' && code !== 'i' && code !== 'r') continue

    events.push({ at, code, data })
  }

  return {
    width,
    height,
    timestamp,
    events,
    duration: events.length > 0 ? events[events.length - 1].at : 0,
  }
}

/** Renders a duration the way a player's clock reads it. */
export function clock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const minutes = Math.floor(total / 60)
  const rest = total % 60
  return `${String(minutes).padStart(2, '0')}:${String(rest).padStart(2, '0')}`
}
