import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { proxyURL, readToken } from '../api/client'

/*
 * The Kubernetes exec channel protocol prefixes every binary frame with the
 * channel it belongs to. KubeMG's proxy pipes these bytes verbatim, so the
 * framing is interpreted here — at the only end that knows what a terminal is.
 */
const CHANNEL_STDIN = 0
const CHANNEL_STDOUT = 1
const CHANNEL_STDERR = 2
const CHANNEL_ERROR = 3
const CHANNEL_RESIZE = 4

/* v4 is what every current API server speaks; v5 adds a stdin close signal. */
const SUBPROTOCOLS = ['v5.channel.k8s.io', 'v4.channel.k8s.io']

/* The shells to try, in order. A distroless container has none of them, and
   saying so beats a blank pane. */
const SHELLS = ['/bin/bash', '/bin/sh']

type Status = 'connecting' | 'open' | 'closed' | 'error'

/** The terminal borrows its palette from the deck rather than hard-coding one. */
function deckTheme() {
  const deck = getComputedStyle(document.documentElement)
  const token = (name: string, fallback: string) =>
    deck.getPropertyValue(name).trim() || fallback

  return {
    background: token('--deck-sunken', '#0d1017'),
    foreground: token('--deck-text', '#e7ebf3'),
    cursor: token('--deck-accent', '#8878fb'),
    selectionBackground: token('--deck-border', '#242b38'),
  }
}

/**
 * PodTerminal is an interactive shell in a container, carried over the same
 * audited tunnel as everything else. Every keystroke reaches the cluster under
 * the caller's own impersonated identity — this is not a back door around the
 * permission model, it is the permission model applied to a terminal.
 */
export function PodTerminal({
  clusterId,
  namespace,
  pod,
  container,
}: {
  clusterId: number
  namespace: string
  pod: string
  container: string
}) {
  const host = useRef<HTMLDivElement | null>(null)
  const [status, setStatus] = useState<Status>('connecting')
  const [detail, setDetail] = useState<string | null>(null)

  useEffect(() => {
    const element = host.current
    if (!element) return

    const term = new Terminal({
      fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12.5,
      lineHeight: 1.35,
      cursorBlink: true,
      convertEol: true,
      theme: deckTheme(),
    })

    // The shell sits on the same slab as every other machine-output surface, so
    // it follows the deck when the operator switches it.
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
    const decoder = new TextDecoder()

    // The token cannot go in a header on a browser WebSocket, so it rides in
    // the query string. The proxy accepts either.
    const token = readToken() ?? ''
    const query = new URLSearchParams({
      container,
      stdin: 'true',
      stdout: 'true',
      stderr: 'true',
      tty: 'true',
      access_token: token,
    })
    for (const shell of SHELLS) {
      query.append('command', shell)
    }

    const url = proxyURL(
      clusterId,
      `/api/v1/namespaces/${encodeURIComponent(namespace)}/pods/${encodeURIComponent(pod)}/exec?${query}`,
      'ws',
    )

    const socket = new WebSocket(url, SUBPROTOCOLS)
    socket.binaryType = 'arraybuffer'

    /** send frames a payload on a channel, as the protocol requires. */
    function send(channel: number, payload: Uint8Array) {
      if (socket.readyState !== WebSocket.OPEN) return
      const frame = new Uint8Array(payload.length + 1)
      frame[0] = channel
      frame.set(payload, 1)
      socket.send(frame)
    }

    function sendResize() {
      send(
        CHANNEL_RESIZE,
        encoder.encode(JSON.stringify({ Width: term.cols, Height: term.rows })),
      )
    }

    socket.onopen = () => {
      setStatus('open')
      sendResize()
      term.focus()
    }

    socket.onmessage = (event) => {
      const frame = new Uint8Array(event.data as ArrayBuffer)
      if (frame.length === 0) return

      const channel = frame[0]
      const payload = decoder.decode(frame.subarray(1))
      switch (channel) {
        case CHANNEL_STDOUT:
        case CHANNEL_STDERR:
          term.write(payload)
          break
        case CHANNEL_ERROR:
          // The API server reports a failed exec on this channel as a JSON
          // Status object; show its message rather than raw JSON.
          try {
            const parsed = JSON.parse(payload) as { status?: string; message?: string }
            if (parsed.status !== 'Success' && parsed.message) {
              term.write(`\r\n\x1b[31m${parsed.message}\x1b[0m\r\n`)
            }
          } catch {
            if (payload.trim()) term.write(`\r\n${payload}\r\n`)
          }
          break
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
  }, [clusterId, namespace, pod, container])

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
            ? `Connected to ${container}`
            : status === 'connecting'
              ? 'Opening a session…'
              : status === 'error'
                ? 'Could not open a session'
                : 'Session closed'}
        </span>
        {detail ? <span className="truncate text-[12px] text-muted">· {detail}</span> : null}
      </div>
      <div
        ref={host}
        className="min-h-[280px] flex-1 overflow-hidden rounded-card border border-line bg-sunken p-2"
      />
    </div>
  )
}
