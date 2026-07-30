import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { Check, Copy, Pause, Play, RotateCcw } from 'lucide-react'
import {
  errorMessage,
  fetchTerminalSessionCast,
  fetchTerminalSessions,
} from '../../api/client'
import type { TerminalSession } from '../../api/types'
import { relativeAge } from '../../lib/time'
import { Chip, DetailList, IconButton, Notice, Pill, Segmented, Select, Slab } from '../primitives'
import { clock, parseCast, type Cast } from './cast'

/** Playback speeds. Faster than 8x stops being watchable and starts being a diff. */
const SPEEDS = [0.5, 1, 2, 4, 8]

/**
 * How long a pause has to be before "skip pauses" jumps it. Two seconds is about
 * where a gap stops being someone typing and starts being someone thinking.
 */
const PAUSE_THRESHOLD = 2

type View = 'terminal' | 'keystrokes'

/** The replay canvas borrows the deck's palette rather than hard-coding one. */
function deckTheme() {
  const deck = getComputedStyle(document.documentElement)
  const token = (name: string, fallback: string) =>
    deck.getPropertyValue(name).trim() || fallback

  return {
    background: token('--deck-sunken', '#0d1017'),
    foreground: token('--deck-text', '#e7ebf3'),
    cursor: token('--deck-sunken', '#0d1017'),
    selectionBackground: token('--deck-border', '#242b38'),
  }
}

/**
 * TerminalSessionPlayer replays a recorded exec or attach session.
 *
 * It is the same emulator the live terminal uses, with stdin turned off — a
 * replay that could accept keystrokes would be a shell, and this is evidence.
 * The recording is read as a whole rather than streamed: a session is capped
 * when it is written, and having every frame in hand is what makes scrubbing
 * backwards possible at all (the terminal is reset and fast-forwarded, because
 * a terminal has no history to rewind).
 *
 * Only output is drawn. Keystrokes are recorded too — that is the audit half —
 * but a pty echoes what was typed back on stdout, so drawing input as well
 * would double every character; they get their own view instead, which is also
 * the only way to see what was typed into a session with no tty.
 */
export function TerminalSessionPlayer({
  sessionId,
  session: known,
}: {
  /** The correlation id an audit row carries. */
  sessionId: string
  /** The row, when the caller already has it; otherwise it is looked up. */
  session?: TerminalSession
}) {
  const host = useRef<HTMLDivElement | null>(null)
  const term = useRef<Terminal | null>(null)

  const [session, setSession] = useState<TerminalSession | null>(known ?? null)
  const [cast, setCast] = useState<Cast | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [view, setView] = useState<View>('terminal')
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState(1)
  const [skipPauses, setSkipPauses] = useState(true)
  const [position, setPosition] = useState(0)
  const [copied, setCopied] = useState(false)

  // The virtual clock and the next unplayed event. They are refs because the
  // animation loop reads them every frame and re-rendering on each is pointless
  // — the scrubber only needs the tenth-of-a-second the label shows.
  const elapsed = useRef(0)
  const cursor = useRef(0)

  useEffect(() => {
    let cancelled = false

    async function load() {
      setLoading(true)
      setError(null)
      try {
        let row = known ?? null
        if (!row || row.session_id !== sessionId) {
          const page = await fetchTerminalSessions({ session_id: sessionId, limit: 1 })
          row = page.sessions[0] ?? null
          if (!row) {
            if (!cancelled) {
              setError(
                page.recording_enabled
                  ? 'This session was not recorded. It may predate recording being switched on.'
                  : 'Session recording is not enabled on this server, so there is nothing to replay.',
              )
              setLoading(false)
            }
            return
          }
        }
        if (!row) return
        const raw = await fetchTerminalSessionCast(row.id)
        if (cancelled) return
        setSession(row)
        setCast(parseCast(raw))
      } catch (err) {
        if (!cancelled) setError(errorMessage(err, 'Could not load this recording.'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [sessionId, known])

  // The emulator lives as long as the recording does. Its geometry is the
  // recording's own, so what plays back is the window the operator had.
  useEffect(() => {
    const element = host.current
    if (!element || !cast || view !== 'terminal') return

    const emulator = new Terminal({
      fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12.5,
      lineHeight: 1.35,
      convertEol: true,
      cursorBlink: false,
      // A replay takes no input. This is the whole difference between the player
      // and the live terminal.
      disableStdin: true,
      scrollback: 5000,
      cols: cast.width,
      rows: cast.height,
      theme: deckTheme(),
    })
    emulator.open(element)
    term.current = emulator

    const deckWatcher = new MutationObserver(() => {
      emulator.options.theme = deckTheme()
    })
    deckWatcher.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    })

    elapsed.current = 0
    cursor.current = 0
    setPosition(0)
    setPlaying(true)

    return () => {
      deckWatcher.disconnect()
      emulator.dispose()
      term.current = null
    }
  }, [cast, view])

  /**
   * seek advances the replay to an absolute offset. Going backwards resets the
   * emulator and replays from the start, which is fast because a fast-forward
   * writes one concatenated string rather than one per frame.
   */
  const seek = useCallback(
    (target: number) => {
      const emulator = term.current
      if (!emulator || !cast) return

      if (target < elapsed.current) {
        emulator.reset()
        cursor.current = 0
      }

      let buffer = ''
      while (cursor.current < cast.events.length && cast.events[cursor.current].at <= target) {
        const event = cast.events[cursor.current]
        cursor.current += 1
        if (event.code === 'o') {
          buffer += event.data
          continue
        }
        if (event.code === 'r') {
          // A resize has to land between the output around it, or the reflow
          // applies to text that was written at the other size.
          if (buffer) {
            emulator.write(buffer)
            buffer = ''
          }
          const [cols, rows] = event.data.split('x').map((part) => Number.parseInt(part, 10))
          if (Number.isFinite(cols) && Number.isFinite(rows) && cols > 0 && rows > 0) {
            emulator.resize(cols, rows)
          }
        }
      }
      if (buffer) emulator.write(buffer)

      elapsed.current = target
      setPosition(target)
    },
    [cast],
  )

  // The playback clock. Wall time scaled by the chosen speed, with long pauses
  // jumped rather than sat through — an operator watching a recording is looking
  // for what happened, not for the four minutes nothing did.
  useEffect(() => {
    if (!playing || !cast || view !== 'terminal') return

    let frame = 0
    let last = performance.now()

    const tick = (now: number) => {
      const advanced = elapsed.current + ((now - last) / 1000) * speed
      last = now

      let next = advanced
      if (skipPauses && cursor.current < cast.events.length) {
        const upcoming = cast.events[cursor.current].at
        if (upcoming - advanced > PAUSE_THRESHOLD) next = upcoming
      }

      seek(Math.min(next, cast.duration))
      if (next >= cast.duration) {
        setPlaying(false)
        return
      }
      frame = requestAnimationFrame(tick)
    }

    frame = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(frame)
  }, [playing, speed, skipPauses, cast, seek, view])

  /** Copy takes what the emulator has rendered, so the clipboard holds text
      rather than the escape sequences that produced it. */
  const copyOutput = useCallback(async () => {
    const emulator = term.current
    if (!emulator) return
    emulator.selectAll()
    const text = emulator.getSelection()
    emulator.clearSelection()
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      setError('The browser would not let this page write to the clipboard.')
    }
  }, [])

  const keystrokes = useMemo(() => {
    if (!cast) return []
    return cast.events
      .filter((event) => event.code === 'i')
      .map((event) => ({ at: event.at, text: readableInput(event.data) }))
  }, [cast])

  if (loading) {
    return <p className="text-[13px] text-muted">Loading the recording…</p>
  }
  if (error && !cast) {
    return <Notice tone="warn">{error}</Notice>
  }
  if (!cast || !session) {
    return <Notice tone="warn">This recording could not be read.</Notice>
  }

  const finished = position >= cast.duration && !playing

  return (
    <div className="flex min-h-0 flex-col gap-3">
      {error ? <Notice tone="warn">{error}</Notice> : null}

      <DetailList
        columns={2}
        rows={[
          { term: 'Who', value: session.username || '—' },
          {
            term: 'Where',
            value: `${session.cluster} · ${session.namespace ?? '—'}/${session.pod_name ?? '—'}`,
          },
          { term: 'Container', value: session.container_name || '—' },
          { term: 'Command', value: session.shell || (session.verb === 'attach' ? 'attach' : '—') },
          { term: 'Started', value: relativeAge(session.started_at) },
          {
            term: 'Ran for',
            value: session.open ? 'still open' : `${session.duration_seconds}s`,
          },
        ]}
      />

      {session.truncated ? (
        <Notice tone="warn">
          This session outgrew the per-recording limit. The replay stops before the session did.
        </Notice>
      ) : null}
      {session.open ? (
        <Notice tone="info">
          This session is still open. The replay covers what had been recorded when it was fetched.
        </Notice>
      ) : null}

      <div className="flex flex-wrap items-center gap-2">
        <Segmented<View>
          ariaLabel="What to show"
          value={view}
          onChange={setView}
          options={[
            { value: 'terminal', label: 'Replay' },
            {
              value: 'keystrokes',
              // "not recorded" and "none" are different answers and the row says
              // which, so the tab does too rather than implying nothing was typed.
              label: session.input_recorded
                ? `Keystrokes${keystrokes.length ? '' : ' (none)'}`
                : 'Keystrokes (not recorded)',
            },
          ]}
        />
        {session.error ? <Pill tone="warn">{session.error}</Pill> : null}
      </div>

      {view === 'keystrokes' ? (
        !session.input_recorded ? (
          /* A policy, not an absence: this server was configured not to collect
             keystrokes, which is what an operator does when people type
             credentials into interactive tools. Reporting it as "nothing was
             typed" would misrepresent the evidence. */
          <Notice tone="info">
            Keystrokes were not recorded for this session — this server captures output only. What
            was typed and echoed back by the shell is still in the replay.
          </Notice>
        ) : keystrokes.length === 0 ? (
          <Notice tone="info">
            Nothing was typed into this session — it was recorded with no stdin, or the operator only
            watched.
          </Notice>
        ) : (
          <Slab className="max-h-[420px]">
            {keystrokes.map((entry, index) => (
              <span key={`${entry.at}-${index}`} className="block">
                <span className="text-muted">{clock(entry.at)} </span>
                {entry.text}
              </span>
            ))}
          </Slab>
        )
      ) : (
        <>
          <div
            ref={host}
            className="min-h-[280px] overflow-auto rounded-card border border-line bg-sunken p-2"
          />

          <div className="flex flex-wrap items-center gap-2.5">
            <IconButton
              label={playing ? 'Pause' : finished ? 'Replay from the start' : 'Play'}
              onClick={() => {
                if (finished) seek(0)
                setPlaying((current) => !current)
              }}
            >
              {playing ? (
                <Pause aria-hidden="true" className="size-4" />
              ) : finished ? (
                <RotateCcw aria-hidden="true" className="size-4" />
              ) : (
                <Play aria-hidden="true" className="size-4" />
              )}
            </IconButton>

            <span className="font-mono text-[12px] text-muted tabular-nums">
              {clock(position)} / {clock(cast.duration)}
            </span>

            <input
              type="range"
              aria-label="Position in the recording"
              min={0}
              max={Math.max(cast.duration, 0.1)}
              step={0.1}
              value={Math.min(position, cast.duration)}
              onChange={(event) => seek(Number(event.target.value))}
              className="h-1 min-w-40 flex-1 cursor-pointer accent-accent"
            />

            <div className="w-20">
              <Select
                aria-label="Playback speed"
                size="sm"
                value={String(speed)}
                onChange={(event) => setSpeed(Number(event.target.value))}
              >
                {SPEEDS.map((value) => (
                  <option key={value} value={value}>
                    {value}×
                  </option>
                ))}
              </Select>
            </div>

            <Chip active={skipPauses} onClick={() => setSkipPauses((current) => !current)}>
              Skip pauses
            </Chip>

            <IconButton label="Copy the terminal output" onClick={() => void copyOutput()}>
              {copied ? (
                <Check aria-hidden="true" className="size-4 text-ok" />
              ) : (
                <Copy aria-hidden="true" className="size-4" />
              )}
            </IconButton>
          </div>
        </>
      )}
    </div>
  )
}

/**
 * readableInput renders recorded keystrokes as something a person can read.
 * Control characters are the point of the input trail — a Ctrl-C matters — so
 * they are named rather than dropped.
 */
function readableInput(data: string): string {
  let out = ''
  for (const char of data) {
    const code = char.codePointAt(0) ?? 0
    if (char === '\r' || char === '\n') {
      out += '⏎'
      continue
    }
    if (char === '\t') {
      out += '⇥'
      continue
    }
    if (char === '') {
      out += '⌫'
      continue
    }
    if (code === 0x08) {
      out += '⌫'
      continue
    }
    if (code < 0x20) {
      // Ctrl-A is 0x01, so the letter is the code plus 64.
      out += `^${String.fromCharCode(code + 64)}`
      continue
    }
    out += char
  }
  return out
}
