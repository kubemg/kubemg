import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { Circle, Lock, ShieldAlert } from 'lucide-react'
import { fetchRecordingPolicy, proxyURL, readToken } from '../api/client'
import type { RecordingPolicy } from '../api/types'
import { Select } from './primitives'

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

/*
 * The shell to exec. Kubernetes takes `command` as an argv, not as a list of
 * candidates — sending both `/bin/bash` and `/bin/sh` does not try one and fall
 * back to the other, it runs bash *with* /bin/sh as its argument. So exactly one
 * is sent, and which one is the operator's choice: bash is the comfortable
 * default, sh is what a distroless or Alpine image actually has.
 */
const SHELLS = [
  { value: '/bin/bash', label: 'bash' },
  { value: '/bin/sh', label: 'sh' },
]

const DEFAULT_SHELL = SHELLS[0].value

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
  const [shell, setShell] = useState(DEFAULT_SHELL)
  // What this server captures. It is read once and shown before the first
  // keystroke: telling somebody afterwards that everything they typed was kept
  // is not disclosure, and in several jurisdictions it is not lawful either.
  // `null` means the answer has not arrived — the notice is drawn only for a
  // definite answer, because a banner that flickers "recorded" and then
  // disappears is worse than one that arrives a moment late.
  const [policy, setPolicy] = useState<RecordingPolicy | null>(null)

  useEffect(() => {
    let cancelled = false
    fetchRecordingPolicy()
      .then((result) => {
        if (!cancelled) setPolicy(result)
      })
      .catch(() => {
        // A server that will not answer is not a reason to refuse a shell; the
        // notice is simply not drawn.
        if (!cancelled) setPolicy(null)
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const element = host.current
    if (!element) return

    // Reconnecting after a closed session starts from "connecting" again, or
    // the header would still be reporting the session that just ended.
    setStatus('connecting')
    setDetail(null)

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

    // A character is not a byte, and a frame boundary does not respect one. "ş"
    // is two bytes and "€" is three, and the pty on the far side makes no promise
    // that they arrive in the same write — so a decoder that treats every frame
    // as a complete string turns the halves into replacement characters, which is
    // what an operator sees as "I cannot type Turkish". Each channel keeps its own
    // streaming decoder instead, carrying an incomplete sequence forward to the
    // frame that finishes it. Per channel rather than one shared decoder, because
    // stdout and stderr are two byte streams interleaved on one socket: a
    // remainder from one must never be completed by the other's first byte.
    const decoders = new Map<number, TextDecoder>()
    function decodeChannel(channel: number, bytes: Uint8Array): string {
      let decoder = decoders.get(channel)
      if (!decoder) {
        decoder = new TextDecoder()
        decoders.set(channel, decoder)
      }
      return decoder.decode(bytes, { stream: true })
    }

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
      command: shell,
    })

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
          // The API server reports a failed exec on this channel as a JSON
          // Status object; show its message rather than raw JSON. It arrives
          // whole, so it is decoded on its own rather than through a streaming
          // decoder that would hold a trailing byte back waiting for more.
          const payload = new TextDecoder().decode(body)
          try {
            const parsed = JSON.parse(payload) as { status?: string; message?: string }
            if (parsed.status !== 'Success' && parsed.message) {
              term.write(`\r\n\x1b[31m${parsed.message}\x1b[0m\r\n`)
              // A slim image often has no bash at all, and "executable file not
              // found" is the moment to say which control fixes it.
              if (parsed.message.includes('executable file not found')) {
                term.write(
                  `\x1b[90mThis image has no ${shell}. Try another shell from the picker above.\x1b[0m\r\n`,
                )
              }
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
    // Changing the shell is a new session: the old one is torn down by the
    // cleanup above and a fresh exec is opened, which is what an operator means
    // by picking a different shell.
  }, [clusterId, namespace, pod, container, shell])

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

        {/* Which shell to exec. It is a session parameter, so changing it opens
            a new session rather than trying to change the running one. */}
        <div className="ml-auto flex items-center gap-2">
          <span className="label">Shell</span>
          <div className="w-28">
            <Select
              aria-label="Shell"
              size="sm"
              value={shell}
              onChange={(event) => setShell(event.target.value)}
            >
              {SHELLS.map((entry) => (
                <option key={entry.value} value={entry.value}>
                  {entry.label}
                </option>
              ))}
            </Select>
          </div>
        </div>
      </div>
      {policy?.enabled ? <RecordingNotice policy={policy} /> : null}

      <div
        ref={host}
        className="min-h-[280px] flex-1 overflow-hidden rounded-card border border-line bg-sunken p-2"
      />
    </div>
  )
}

/**
 * RecordingNotice tells an operator what is being captured, before they type.
 *
 * It is a line rather than a dialog on purpose: a shell that has to be
 * acknowledged before it opens would be dismissed by reflex within a week, and
 * the point is that the fact stays visible for the whole session. What it says is
 * specific — keystrokes or only output, encrypted at rest or not, and for how
 * long — because "this session may be monitored" is the kind of notice people
 * stop reading.
 */
function RecordingNotice({ policy }: { policy: RecordingPolicy }) {
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-control border border-line-soft bg-raised/60 px-2.5 py-1.5 text-[12px] text-muted">
      <Circle aria-hidden="true" className="size-3 shrink-0 fill-danger text-danger" />
      <span className="text-fg">This session is being recorded.</span>
      <span>
        {policy.input_recorded
          ? 'Output and keystrokes are captured'
          : 'Output is captured; keystrokes are not'}
        {policy.retention_days > 0 ? ` and kept for ${policy.retention_days} days` : ''}.
      </span>
      {policy.encrypted ? (
        <span className="inline-flex items-center gap-1 text-faint">
          <Lock aria-hidden="true" className="size-3 shrink-0" />
          encrypted at rest
        </span>
      ) : (
        /* Said plainly, because it changes what an operator should be willing to
           type: an unencrypted recording is a plaintext file holding whatever a
           prompt did not echo. */
        <span className="inline-flex items-center gap-1 text-warn">
          <ShieldAlert aria-hidden="true" className="size-3 shrink-0" />
          not encrypted at rest
        </span>
      )}
    </p>
  )
}
