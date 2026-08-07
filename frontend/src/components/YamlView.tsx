import { useEffect, useMemo, useRef } from 'react'
import type { ReactNode } from 'react'

/*
 * A YAML surface that reads and edits in the same place. Highlighting is a
 * `<pre>` sitting under a transparent `<textarea>` — the two share a font, a
 * padding and a line height, so the caret lands exactly where the glyph is.
 * That is deliberately not a code-editor dependency: the heaviest thing in this
 * app is already a terminal nobody loads unless they open a shell, and a
 * manifest viewer is not worth another 300 kB on every session.
 *
 * The palette is the deck's own. Syntax colour is carried by *tier* — keys take
 * the accent, structure and comments recede — rather than by the red/amber/green
 * a normal highlighter would reach for, because in this console those three mean
 * health and nothing else. The single exception is `syntax-scalar`: a replica
 * count, a port, a `true` is what an operator scans a manifest for, and a value
 * that is not a string cannot be told from one by the text tiers alone.
 *
 * Tokenizing is per line and deliberately shallow — this is a highlighter, not a
 * parser, and a manifest that is already valid enough for the cluster to have
 * returned it needs no error recovery. The one piece of document state it does
 * keep is the block scalar: everything indented under `data: |` is somebody
 * else's file, and painting an nginx.conf as if its colons were YAML keys is
 * worse than leaving it plain.
 */

type Token = { text: string; className: string }

const KEY = 'text-accent'
const VALUE = 'text-fg'
const SCALAR = 'text-syntax-scalar'
const PUNCT = 'text-muted'
const FAINT = 'text-faint'

/** A scalar YAML resolves to something other than a string. */
const LITERAL = /^(?:true|false|yes|no|on|off|null|~|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)$/i
/** An anchor, an alias or a tag: structure attached to a value, not the value. */
const MARKER = /^(?:[&*][^\s]+|!!?[^\s]*)$/
/** `|`, `>`, and their chomping/indentation indicators. */
const BLOCK = /^[|>][+-]?\d*$|^[|>]\d*[+-]?$/

/** tokenizeLine splits one YAML line into the few things worth telling apart. */
function tokenizeLine(line: string): Token[] {
  if (line.trim() === '') return [{ text: line, className: VALUE }]

  const indent = line.slice(0, line.length - line.trimStart().length)
  const rest = line.slice(indent.length)

  // A whole-line comment, and the document separator, are structure rather than
  // content: both recede.
  if (rest.startsWith('#') || rest === '---' || rest === '...') {
    return [{ text: line, className: FAINT }]
  }

  const tokens: Token[] = []
  if (indent) tokens.push({ text: indent, className: VALUE })

  let body = rest
  // A sequence entry can carry a key of its own ("- name: web"), so the dash is
  // peeled off before looking for one. Nested entries ("- - a") peel each.
  while (body.startsWith('- ') || body === '-') {
    if (body === '-') {
      tokens.push({ text: '-', className: PUNCT })
      body = ''
      break
    }
    tokens.push({ text: '- ', className: PUNCT })
    body = body.slice(2)
  }

  const colon = keyEnd(body)
  if (colon > 0) {
    tokens.push({ text: body.slice(0, colon), className: KEY })
    tokens.push({ text: ':', className: PUNCT })
    body = body.slice(colon + 1)
  }

  if (body !== '') tokens.push(...tokenizeValue(body))
  return tokens
}

/**
 * keyEnd finds the colon that ends a mapping key, or -1. A colon only ends a key
 * when a space or the end of the line follows it — `image: nginx:1.27` has one
 * key and a tag that is not one. A quoted key keeps its quotes, so the scan
 * starts past them rather than giving up: `"app.kubernetes.io/name": web` is a
 * key like any other.
 */
function keyEnd(body: string): number {
  let start = 0
  const quote = body[0]
  if (quote === '"' || quote === "'") {
    const close = closingQuote(body, quote)
    if (close < 0) return -1
    start = close + 1
  }
  for (let i = start; i < body.length; i += 1) {
    if (body[i] !== ':') continue
    if (i + 1 === body.length || body[i + 1] === ' ') return i
    return -1
  }
  return -1
}

/** closingQuote finds the end of a quoted scalar starting at 0, or -1. */
function closingQuote(body: string, quote: string): number {
  for (let i = 1; i < body.length; i += 1) {
    if (quote === '"' && body[i] === '\\') {
      i += 1
      continue
    }
    if (body[i] !== quote) continue
    // In single quotes a doubled quote is an escaped one, not the end.
    if (quote === "'" && body[i + 1] === "'") {
      i += 1
      continue
    }
    return i
  }
  return -1
}

/**
 * tokenizeValue paints what follows a key. It separates a trailing comment,
 * keeps a quoted string whole (a `#` inside quotes is not a comment), and tells
 * a scalar from a string, which is the distinction the extra hue exists for.
 */
function tokenizeValue(body: string): Token[] {
  const lead = body.length - body.trimStart().length
  const tokens: Token[] = []
  if (lead) tokens.push({ text: body.slice(0, lead), className: VALUE })

  let value = body.slice(lead)
  if (value === '') return tokens

  const quote = value[0]
  if (quote === '"' || quote === "'") {
    const close = closingQuote(value, quote)
    const end = close < 0 ? value.length : close + 1
    tokens.push({ text: value.slice(0, end), className: VALUE })
    const trailing = value.slice(end)
    if (trailing) tokens.push({ text: trailing, className: FAINT })
    return tokens
  }

  const comment = value.indexOf(' #')
  let tail = ''
  if (comment >= 0) {
    tail = value.slice(comment)
    value = value.slice(0, comment)
  }

  // A plain value can be several words ("command: sleep 3600"), and only some of
  // them are scalars, so each is classified on its own.
  for (const piece of value.split(/(\s+)/)) {
    if (piece === '') continue
    if (/^\s+$/.test(piece)) tokens.push({ text: piece, className: VALUE })
    else if (BLOCK.test(piece) || /^[[\]{},]+$/.test(piece))
      tokens.push({ text: piece, className: PUNCT })
    else if (MARKER.test(piece)) tokens.push({ text: piece, className: PUNCT })
    else if (LITERAL.test(piece)) tokens.push({ text: piece, className: SCALAR })
    else tokens.push({ text: piece, className: VALUE })
  }

  if (tail) tokens.push({ text: tail, className: FAINT })
  return tokens
}

/** blockScalarAt reports the indent a block scalar's body sits under, or -1 —
    the last non-space token on the line being `|`/`>` is what opens one. */
function blockScalarAt(line: string): number {
  const trimmed = line.trimEnd()
  const last = trimmed.slice(trimmed.lastIndexOf(' ') + 1)
  if (!BLOCK.test(last)) return -1
  return line.length - line.trimStart().length
}

/** paint renders a whole document. A line in a text buffer has no identity of
    its own, so its position is genuinely what identifies it. */
function paint(source: string): ReactNode[] {
  // -1 when no block scalar is open; otherwise the indent of the key that opened
  // it. Its body is anything indented further, and any blank line inside it.
  let blockIndent = -1

  return source.split('\n').map((line, index) => {
    const indent = line.length - line.trimStart().length
    const inBlock = blockIndent >= 0 && (line.trim() === '' || indent > blockIndent)

    let tokens: Token[]
    if (inBlock) {
      tokens = [{ text: line, className: VALUE }]
    } else {
      blockIndent = -1
      tokens = tokenizeLine(line)
      const opened = blockScalarAt(line)
      if (opened >= 0) blockIndent = opened
    }

    return (
      <span key={index}>
        {tokens.map((token, position) => (
          <span key={position} className={token.className}>
            {token.text}
          </span>
        ))}
        {'\n'}
      </span>
    )
  })
}

const TYPE = 'm-0 py-2.5 font-mono text-[12px] leading-[1.55] whitespace-pre'
const SURFACE = `${TYPE} h-full w-full`
/* The gutter is sized in `ch`, so it holds four digits of the mono face exactly
   and the text column starts on the same pixel in both layers. It carries the
   surface's own background because the text column scrolls sideways underneath
   it — a transparent gutter would have a long line running through the numbers. */
const GUTTER = `${TYPE} pr-2 pl-2`
const PAD = 'px-3'
/** The number column is as wide as the document's own line count needs, so a
    long embedded file does not lose a digit to a fixed column. */
function gutterWidth(count: number): string {
  return `${Math.max(2, String(count).length)}ch`
}

/**
 * YamlView shows a manifest, and edits it when `onChange` is given. Read-only is
 * the default on purpose: most visits to a manifest are a question, not a change.
 * The caller sizes it — the surface fills whatever box it is put in.
 */
export function YamlView({
  value,
  onChange,
  className,
  /**
   * Line numbers. Worth their column wherever the cluster can answer with one —
   * an API server refusing a manifest says which line it choked on — and not
   * worth it for a short block nobody is going to be told a line number about.
   */
  numbered = true,
}: {
  value: string
  onChange?: (next: string) => void
  className?: string
  numbered?: boolean
}) {
  const editing = onChange !== undefined
  const input = useRef<HTMLTextAreaElement | null>(null)
  const layer = useRef<HTMLPreElement | null>(null)
  const gutter = useRef<HTMLPreElement | null>(null)

  // Neither the highlight layer nor the gutter scrolls on its own — both follow
  // whichever layer the caret lives in, which is the textarea when editing and
  // the painted layer when not.
  useEffect(() => {
    const scroller: HTMLElement | null = editing ? input.current : layer.current
    if (!scroller) return

    function sync() {
      if (!scroller) return
      if (editing && layer.current) {
        layer.current.scrollTop = scroller.scrollTop
        layer.current.scrollLeft = scroller.scrollLeft
      }
      if (gutter.current) gutter.current.scrollTop = scroller.scrollTop
    }
    scroller.addEventListener('scroll', sync)
    return () => scroller.removeEventListener('scroll', sync)
  }, [editing])

  const painted = useMemo(() => paint(value), [value])
  const lines = useMemo(() => {
    if (!numbered) return null
    const count = value.split('\n').length
    const text = Array.from({ length: count }, (_, index) => `${index + 1}\n`).join('')
    return { text, width: gutterWidth(count) }
  }, [numbered, value])

  const pad = lines ? 'pr-3' : PAD
  // Left padding tracks the gutter, so it is a style rather than a class: the
  // width is the document's, and Tailwind reads the source for literal names.
  const padStyle = lines ? { paddingLeft: `calc(${lines.width} + 1.5rem)` } : undefined

  return (
    <div
      className={`relative overflow-hidden rounded-control border border-line bg-sunken ${className ?? ''}`}
    >
      {lines ? (
        <pre
          ref={gutter}
          aria-hidden="true"
          style={{ width: `calc(${lines.width} + 1rem)` }}
          className={`${GUTTER} pointer-events-none absolute inset-y-0 left-0 overflow-hidden border-r border-line-soft bg-sunken text-right text-faint select-none`}
        >
          {lines.text}
        </pre>
      ) : null}

      <pre
        ref={layer}
        aria-hidden={editing ? 'true' : undefined}
        style={padStyle}
        className={`${SURFACE} ${pad} ${editing ? 'absolute inset-0 overflow-hidden' : 'overflow-auto'}`}
      >
        {painted}
      </pre>

      {editing ? (
        <textarea
          ref={input}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          aria-label="Resource manifest"
          style={padStyle}
          className={`${SURFACE} ${pad} relative resize-none overflow-auto bg-transparent text-transparent caret-fg outline-none`}
        />
      ) : null}
    </div>
  )
}
