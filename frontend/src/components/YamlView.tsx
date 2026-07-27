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
 * the accent, values the foreground, structure and comments recede — rather than
 * by the red/amber/green a normal highlighter would reach for, because in this
 * console those three mean health and nothing else.
 */

type Token = { text: string; className: string }

const KEY = 'text-accent'
const VALUE = 'text-fg'
const PUNCT = 'text-muted'
const FAINT = 'text-faint'

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
  // peeled off before looking for one.
  if (body.startsWith('- ')) {
    tokens.push({ text: '- ', className: PUNCT })
    body = body.slice(2)
  } else if (body === '-') {
    tokens.push({ text: '-', className: PUNCT })
    body = ''
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
 * key and a tag that is not one.
 */
function keyEnd(body: string): number {
  if (body.startsWith('"') || body.startsWith("'")) return -1
  for (let i = 0; i < body.length; i += 1) {
    if (body[i] !== ':') continue
    if (i + 1 === body.length || body[i + 1] === ' ') return i
    return -1
  }
  return -1
}

/** tokenizeValue separates a trailing comment from the value it follows. */
function tokenizeValue(body: string): Token[] {
  const comment = body.indexOf(' #')
  if (comment < 0) return [{ text: body, className: VALUE }]
  return [
    { text: body.slice(0, comment), className: VALUE },
    { text: body.slice(comment), className: FAINT },
  ]
}

/** paint renders a whole document. A line in a text buffer has no identity of
    its own, so its position is genuinely what identifies it. */
function paint(source: string): ReactNode[] {
  return source.split('\n').map((line, index) => (
    <span key={index}>
      {tokenizeLine(line).map((token, position) => (
        <span key={position} className={token.className}>
          {token.text}
        </span>
      ))}
      {'\n'}
    </span>
  ))
}

const SURFACE = 'm-0 h-full w-full px-3 py-2.5 font-mono text-[12px] leading-[1.55] whitespace-pre'

/**
 * YamlView shows a manifest, and edits it when `onChange` is given. Read-only is
 * the default on purpose: most visits to a manifest are a question, not a change.
 * The caller sizes it — the surface fills whatever box it is put in.
 */
export function YamlView({
  value,
  onChange,
  className,
}: {
  value: string
  onChange?: (next: string) => void
  className?: string
}) {
  const editing = onChange !== undefined
  const input = useRef<HTMLTextAreaElement | null>(null)
  const layer = useRef<HTMLPreElement | null>(null)

  // The highlight layer does not scroll on its own — it follows the textarea,
  // which is the layer the caret lives in.
  useEffect(() => {
    const area = input.current
    if (!area) return

    function sync() {
      if (!area || !layer.current) return
      layer.current.scrollTop = area.scrollTop
      layer.current.scrollLeft = area.scrollLeft
    }
    area.addEventListener('scroll', sync)
    return () => area.removeEventListener('scroll', sync)
  }, [editing])

  const painted = useMemo(() => paint(value), [value])

  return (
    <div
      className={`relative overflow-hidden rounded-control border border-line bg-sunken ${className ?? ''}`}
    >
      <pre
        ref={layer}
        aria-hidden={editing ? 'true' : undefined}
        className={`${SURFACE} ${editing ? 'absolute inset-0 overflow-hidden' : 'overflow-auto'}`}
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
          className={`${SURFACE} relative resize-none overflow-auto bg-transparent text-transparent caret-fg outline-none`}
        />
      ) : null}
    </div>
  )
}
