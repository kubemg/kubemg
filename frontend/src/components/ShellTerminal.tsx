import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { fetchRecordingPolicy, shellSocketURL } from '../api/client'
import type { RecordingPolicy } from '../api/types'
import { RecordingNotice } from './terminal/RecordingNotice'

/*
 * The browser shell's terminal.
 *
 * It is the pod terminal's twin and deliberately not a generalisation of it: the
 * two differ in exactly the ways that matter — this one addresses a session
 * rather than a container, and it offers no shell picker, because the image it
 * attaches to is KubeMG's own and is known to have `sh`. Folding them together
 * would mean a component whose every branch is "which of the two am I", for the
 * sake of the fifty lines of channel decoding they share.
 *
 * The wire format is the same, because it is the API server's: every frame is a
 * channel byte followed by its payload.
 */
const CHANNEL_STDIN = 0
const CHANNEL_STDOUT = 1
const CHANNEL_STDERR = 2
const CHANNEL_ERROR = 3
const CHANNEL_RESIZE = 4

const SUBPROTOCOLS = ['v4.channel.k8s.io']

type Status = 'connecting' | 'open' | 'closed' | 'error'

/** The terminal borrows its palette from the deck rather than hard-coding one. */
function deckTheme() {
  const deck = getComputedStyle(document.documentElement)
  const token = (name: string, fallback: string) =>
    deck.getPropertyValue(name).trim() || fallback

  return {
    background: token('--deck-sunken', '#101215'),
    foreground: token('--deck-text', '#f2f3ef'),
    cursor: token('--deck-accent', '#bff23c'),
    selectionBackground: token('--deck-border', '#3a4033'),
  }
}

/**
 * ShellTerminal attaches to the caller's shell pod on a cluster.
 *
 * Every command typed here runs as `kubectl` inside that pod, against a
 * kubeconfig pointing back at KubeMG — so it is impersonated as the caller,
 * answered by the cluster's own RBAC and audited. The terminal itself is an exec
 * like any other: recorded, and guarded keystroke by keystroke.
 */
export function ShellTerminal({
  clusterId,
  onEnded,
}: {
  clusterId: number
  /** Called when the session closes, so the page can re-read the pod's state. */
  onEnded?: () => void
}) {
  const host = useRef<HTMLDivElement | null>(null)
  const [status, setStatus] = useState<Status>('connecting')
  const [detail, setDetail] = useState<string | null>(null)
  const [policy, setPolicy] = useState<RecordingPolicy | null>(null)

  // What this server captures, read once and shown before the first keystroke.
  // A server that will not answer is not a reason to refuse a shell; the notice
  // is simply not drawn.
  useEffect(() => {
    let cancelled = false
    fetchRecordingPolicy()
      .then((result) => {
        if (!cancelled) setPolicy(result)
      })
      .catch(() => {
        if (!cancelled) setPolicy(null)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // onEnded is held in a ref so that a parent re-rendering it does not tear the
  // session down: a terminal that reconnected whenever its page re-rendered
  // would lose whatever was on the prompt.
  const ended = useRef(onEnded)
  ended.current = onEnded

  useEffect(() => {
    const element = host.current
    if (!element) return

    setStatus('connecting')
    setDetail(null)

    const term = new Terminal({
      fontFamily: '"Commit Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12.5,
      lineHeight: 1.35,
      cursorBlink: true,
      convertEol: true,
      theme: deckTheme(),
    })

    const deckWatcher = new MutationObserver(() => {
      term.options.theme = deckTheme()
    })
    deckWatcher.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    })

    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(element)
    fit.fit()

    const encoder = new TextEncoder()

    // One streaming decoder per channel: a multi-byte character can be split
    // across two frames, and stdout and stderr are two byte streams interleaved
    // on one socket — a remainder from one must never be completed by the
    // other's first byte.
    const decoders = new Map<number, TextDecoder>()
    function decodeChannel(channel: number, bytes: Uint8Array): string {
      let decoder = decoders.get(channel)
      if (!decoder) {
        decoder = new TextDecoder()
        decoders.set(channel, decoder)
      }
      return decoder.decode(bytes, { stream: true })
    }

    const socket = new WebSocket(shellSocketURL(clusterId), SUBPROTOCOLS)
    socket.binaryType = 'arraybuffer'

    function send(channel: number, payload: Uint8Array) {
      if (socket.readyState !== WebSocket.OPEN) return
      const frame = new Uint8Array(payload.length + 1)
      frame[0] = channel
      frame.set(payload, 1)
      socket.send(frame)
    }

    function sendResize() {
      send(CHANNEL_RESIZE, encoder.encode(JSON.stringify({ Width: term.cols, Height: term.rows })))
    }

    socket.onopen = () => {
      setStatus('open')
      setDetail(null)
      sendResize()
      term.focus()
    }

    socket.onmessage = (event) => {
      const frame = new Uint8Array(event.data as ArrayBuffer)
      if (frame.length === 0) return

      const channel = frame[0]
      const body = frame.subarray(1)
      switch (channel) {
        case CHANNEL_STDOUT:
        case CHANNEL_STDERR:
          term.write(decodeChannel(channel, body))
          break
        case CHANNEL_ERROR: {
          const payload = new TextDecoder().decode(body)
          try {
            const parsed = JSON.parse(payload) as { status?: string; message?: string }
            if (parsed.status !== 'Success' && parsed.message) {
              term.write(`\r\n\x1b[31m${parsed.message}\x1b[0m\r\n`)
            }
          } catch {
            if (payload.trim()) term.write(`\r\n${payload}\r\n`)
          }
          break
        }
        default:
          break
      }
    }

    socket.onerror = () => {
      setStatus('error')
      setDetail('The session could not be established.')
    }

    socket.onclose = (event) => {
      setStatus((current) => (current === 'error' ? current : 'closed'))
      if (event.reason) setDetail(event.reason)
      term.write('\r\n\x1b[90m— session ended —\x1b[0m\r\n')
      ended.current?.()
    }

    const typed = term.onData((data) => send(CHANNEL_STDIN, encoder.encode(data)))

    const observer = new ResizeObserver(() => {
      fit.fit()
      sendResize()
    })
    observer.observe(element)

    return () => {
      observer.disconnect()
      deckWatcher.disconnect()
      typed.dispose()
      // 1000 is a normal close; anything else makes the audit trail read as if
      // the session crashed.
      if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
        socket.close(1000, 'closed by the operator')
      }
      term.dispose()
    }
  }, [clusterId])

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex items-center gap-2">
        <span
          aria-hidden="true"
          className={`inline-block size-1.5 rounded-full ${
            status === 'open'
              ? 'breathe bg-ok'
              : status === 'connecting'
                ? 'bg-warn'
                : status === 'error'
                  ? 'bg-danger'
                  : 'bg-faint'
          }`}
        />
        <span className="text-[12px] text-muted">
          {status === 'open'
            ? 'Connected'
            : status === 'connecting'
              ? 'Opening a session…'
              : status === 'error'
                ? 'Could not open a session'
                : 'Session closed'}
        </span>
        {detail ? <span className="truncate text-[12px] text-muted">· {detail}</span> : null}
      </div>

      {policy?.enabled ? <RecordingNotice policy={policy} /> : null}

      <div
        ref={host}
        className="min-h-[320px] flex-1 overflow-hidden rounded-card border border-line bg-sunken p-2"
      />
    </div>
  )
}
