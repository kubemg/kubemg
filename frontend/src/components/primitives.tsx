import { createContext, useCallback, useContext, useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  PointerEvent as ReactPointerEvent,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react'
import { Link } from 'react-router'
import type { LucideIcon } from 'lucide-react'
import {
  Check,
  CircleAlert,
  CircleCheck,
  ChevronDown,
  ChevronUp,
  ChevronsUpDown,
  Copy,
  Eye,
  EyeOff,
  Info,
  Loader2,
  MoreVertical,
  Search,
  TriangleAlert,
  X,
} from 'lucide-react'
import type { Cluster, Environment } from '../api/types'
import { TONE_FILL, TONE_SOFT, clusterStateLabel, clusterTone } from '../lib/status'
import { formatInstant, relativeAge } from '../lib/time'
import type { Tone } from '../lib/status'
import { usageTone } from '../lib/units'

/* ------------------------------------------------------------------ state --- */

// A Pill is the smallest of the soft plates, and it must not disagree with the
// larger ones about what a tone looks like — so the pairing lives in one place.
const TONE_CHIP = TONE_SOFT

// Its edge is the tone at hairline strength, the way the environment tag draws
// its own: a soft fill alone barely separates from a striped row.
const TONE_EDGE: Record<Tone, string> = {
  ok: 'border-ok/40',
  warn: 'border-warn/40',
  bad: 'border-danger/40',
  idle: 'border-faint/60',
  accent: 'border-accent/40',
}

const TONE_DOT: Record<Tone, string> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-danger',
  idle: 'bg-faint',
  accent: 'bg-accent',
}

/**
 * Avatar is a person (or a machine) as two letters in a round chip — the
 * sidebar's own account card, the activity feed and the user lists all draw
 * the same one, so an account looks like itself wherever it turns up.
 */
export function Avatar({ name, className }: { name: string; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`grid size-8 shrink-0 place-items-center rounded-full border border-line-soft bg-raised text-[11.5px] font-semibold text-fg ${className ?? ''}`}
    >
      {name.slice(0, 2).toUpperCase()}
    </span>
  )
}

/** Pill is the compact state chip: a dot plus a word, never colour alone. */
export function Pill({
  tone,
  dot = true,
  children,
  title,
}: {
  tone: Tone
  dot?: boolean
  children: ReactNode
  title?: string
}) {
  return (
    <span
      title={title}
      className={`inline-flex min-w-0 items-center gap-1.5 rounded-full border px-2 py-px text-[12px] font-medium whitespace-nowrap ${TONE_CHIP[tone]} ${TONE_EDGE[tone]}`}
    >
      {dot ? (
        <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${TONE_DOT[tone]}`} />
      ) : null}
      {/* A pill shrinks and ellipsises rather than growing past whatever holds
          it. Its own width is its content's, so in a cell narrower than the
          longest state it names — `CrashLoopBackOff` in a phase column sized for
          `Running` — an unshrinkable pill pushed whatever sat beside it into the
          next column, and a ready count landed against a CPU reading as one
          number: `2/2` beside `55m` read `2/255m`. `min-w-0` is what lets it
          lose the argument, and the ellipsis is what says it did. */}
      <span className="truncate">{children}</span>
    </span>
  )
}

/**
 * Age renders a relative timestamp with the exact instant on hover. The text is
 * the relative form ("5m ago", "in 20h"); hovering shows the full ISO-ordered
 * absolute time with zone, as stated in the `formatInstant` contract. `never` is
 * plain text — there is no useful `<time>` element to wrap around nothing.
 */
export function Age({ iso }: { iso: string | undefined }) {
  if (!iso) return <span className="whitespace-nowrap tabular-nums">never</span>
  return (
    <time dateTime={iso} title={formatInstant(iso)} className="whitespace-nowrap tabular-nums">
      {relativeAge(iso)}
    </time>
  )
}

/**
 * ClusterState renders whether a cluster can be reached, from the same
 * derivation every glyph in the console uses (`lib/status.ts`) rather than from
 * the stored check — the two disagreeing on one screen is what this replaced.
 *
 * The stored check is still worth having and keeps its own place: it is how old
 * the *last probe* is, and it goes in the title beside whatever the probe said,
 * so an answer from nine minutes ago is never presented as an answer from now.
 * For an agent-mode cluster the reading itself is live — the tunnel either has a
 * connection this second or it does not — and the title says so.
 */
export function ClusterState({ cluster }: { cluster: Cluster }) {
  const provenance =
    cluster.connection_mode === 'agent'
      ? 'Read from the tunnel, now.'
      : `Last checked ${relativeAge(cluster.last_checked_at)}.`
  return (
    <Pill
      tone={clusterTone(cluster)}
      title={[provenance, cluster.status_message].filter(Boolean).join(' ')}
    >
      {clusterStateLabel(cluster)}
    </Pill>
  )
}

/** ActivityTag renders whether an account may sign in. */
export function ActivityTag({ active }: { active: boolean }) {
  return <Pill tone={active ? 'ok' : 'idle'}>{active ? 'Active' : 'Disabled'}</Pill>
}

/*
 * Prod and staging carry a colour, so their hairline only has to agree with the
 * word inside it. Dev has no colour by design — it is the ordinary case, and the
 * deck spends colour on states rather than on labels — which leaves the border
 * as the only thing making it read as a tag at all. `line` is the hairline
 * between two planes and is a whisper by construction, so on the tree's own
 * raised surface, where this tag is read most, the chip had no visible edge on
 * the light deck: a grey word floating beside a version number. `faint` at 60%
 * is the same neutral one step up, which is enough to draw the chip without
 * making the quietest environment the loudest mark in the row.
 */
const ENVIRONMENT_TAG: Record<Environment, string> = {
  prod: 'border-danger/40 bg-danger-soft text-danger',
  staging: 'border-warn/40 bg-warn-soft text-warn',
  dev: 'border-faint/60 bg-raised text-muted',
}

/* The environment as a word, not a code: sentence case, the way every other
   label on the deck is now set. */
const ENVIRONMENT_WORD: Record<Environment, string> = {
  prod: 'Prod',
  staging: 'Staging',
  dev: 'Dev',
}

const ENVIRONMENT_DOT: Record<Environment, string> = {
  prod: 'bg-danger',
  staging: 'bg-warn',
  dev: 'bg-faint',
}

export function EnvironmentTag({ environment }: { environment: Environment }) {
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-px text-[11.5px] font-semibold ${ENVIRONMENT_TAG[environment]}`}
    >
      {ENVIRONMENT_WORD[environment]}
    </span>
  )
}

export function EnvironmentDot({ environment }: { environment: Environment }) {
  return (
    <span
      aria-hidden="true"
      className={`inline-block size-1.5 shrink-0 rounded-full ${ENVIRONMENT_DOT[environment]}`}
    />
  )
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 aria-hidden="true" className={`animate-spin ${className ?? 'size-4'}`} />
}

/* ---------------------------------------------------------------- actions --- */

const BUTTON_SIZE = {
  sm: 'h-8 gap-1.5 px-2.5 text-[13px]',
  md: 'h-9 gap-2 px-3.5 text-[13.5px]',
}

const BUTTON_VARIANT = {
  /* Lime with ink on it, on both decks — the brand's one saturated moment, and
     never white text. The hairline is `accent`, which is lime on the dark deck
     and so invisible there, and the darkened lime on the light one, where the
     fill sits at 1.25:1 against bone and cannot delimit itself. */
  primary: 'border border-accent bg-accent-fill text-on-accent hover:bg-accent-fill-hover',
  secondary: 'border border-line bg-surface text-fg hover:border-faint/60 hover:bg-raised',
  ghost: 'text-muted hover:bg-raised hover:text-fg',
  danger: 'border border-danger/40 text-danger hover:bg-danger-soft hover:border-danger/70',
  /* A secondary action sitting on the slab — bone on the dark plate, which is
     dark on both decks, so it does not borrow the page's surface. */
  slab: 'border border-slab-text/35 bg-slab-text/8 text-slab-text hover:bg-slab-text/16',
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: keyof typeof BUTTON_VARIANT
  size?: keyof typeof BUTTON_SIZE
  /** The template's pill: for a dialog's answer and a slab's actions. */
  pill?: boolean
}

export function Button({
  variant = 'secondary',
  size = 'md',
  pill = false,
  className,
  children,
  ...rest
}: ButtonProps) {
  return (
    <button
      {...rest}
      className={`inline-flex shrink-0 cursor-pointer items-center justify-center font-medium whitespace-nowrap transition-colors duration-300 disabled:cursor-not-allowed disabled:opacity-45 ${pill ? 'rounded-full px-4' : 'rounded-control'} ${BUTTON_SIZE[size]} ${BUTTON_VARIANT[variant]} ${className ?? ''}`}
    >
      {children}
    </button>
  )
}

/**
 * LinkButton is navigation drawn as a button: a real link (so it opens in a new
 * tab and reads as one to a screen reader), with a button's look. A `<Button>`
 * inside a `<Link>` is two interactive elements nested, which is invalid.
 */
export function LinkButton({
  to,
  variant = 'secondary',
  size = 'md',
  pill = false,
  className,
  children,
}: {
  to: string
  variant?: keyof typeof BUTTON_VARIANT
  size?: keyof typeof BUTTON_SIZE
  pill?: boolean
  className?: string
  children: ReactNode
}) {
  return (
    <Link
      to={to}
      className={`inline-flex shrink-0 items-center justify-center font-medium whitespace-nowrap transition-colors duration-300 ${pill ? 'rounded-full px-4' : 'rounded-control'} ${BUTTON_SIZE[size]} ${BUTTON_VARIANT[variant]} ${className ?? ''}`}
    >
      {children}
    </Link>
  )
}

/** IconButton is a bare action in a dense row: always titled, never unlabelled. */
export function IconButton({
  label,
  tone = 'neutral',
  className,
  children,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  label: string
  tone?: 'neutral' | 'danger'
}) {
  const hover =
    tone === 'danger' ? 'hover:bg-danger-soft hover:text-danger' : 'hover:bg-raised hover:text-fg'

  return (
    <button
      {...rest}
      title={label}
      className={`inline-grid size-8 shrink-0 cursor-pointer place-items-center rounded-control text-muted transition-colors disabled:cursor-not-allowed disabled:opacity-40 ${hover} ${className ?? ''}`}
    >
      {children}
      <span className="sr-only">{label}</span>
    </button>
  )
}

/**
 * RowMenu folds a row's actions behind one trigger, for a table dense enough
 * that spelling every action out as its own icon button crowds the column the
 * row actually exists to show — a pod's name, on a narrow deck. It carries no
 * state of its own past open/closed: the caller's `RowMenuItem`s do the work.
 *
 * The menu itself is portalled and `fixed`-positioned off the trigger's own
 * rect rather than `absolute` inside the row: every table sits in a
 * `min-w-0 overflow-x-auto` wrapper (`Table` in this file), and once one axis
 * of `overflow` is constrained the browser forces the other to `auto` too —
 * an `absolute` menu would be clipped by that implicit vertical scrollbar
 * instead of floating over the page.
 */
export function RowMenu({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const [rect, setRect] = useState<{ top: number; right: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (!open) return
    function onPointerDown(event: MouseEvent) {
      const target = event.target as Node
      if (triggerRef.current?.contains(target)) return
      if (!menuRef.current?.contains(target)) setOpen(false)
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') setOpen(false)
    }
    function onReposition() {
      const box = triggerRef.current?.getBoundingClientRect()
      if (box) setRect({ top: box.bottom + 4, right: window.innerWidth - box.right })
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('scroll', onReposition, true)
    window.addEventListener('resize', onReposition)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('scroll', onReposition, true)
      window.removeEventListener('resize', onReposition)
    }
  }, [open])

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => {
          const box = triggerRef.current?.getBoundingClientRect()
          if (box) setRect({ top: box.bottom + 4, right: window.innerWidth - box.right })
          setOpen((current) => !current)
        }}
        className="inline-grid size-8 shrink-0 place-items-center rounded-control text-muted transition-colors hover:bg-raised hover:text-fg"
      >
        <MoreVertical aria-hidden="true" className="size-3.5" />
        <span className="sr-only">{label}</span>
      </button>
      {open && rect
        ? createPortal(
            <div
              ref={menuRef}
              role="menu"
              aria-label={label}
              onClick={() => setOpen(false)}
              style={{ top: rect.top, right: rect.right }}
              className="pop-in card fixed z-40 flex w-44 flex-col gap-0.5 p-1 lift"
            >
              {children}
            </div>,
            document.body,
          )
        : null}
    </>
  )
}

/** One row of a `RowMenu`. */
export function RowMenuItem({
  onClick,
  danger,
  children,
}: {
  onClick: () => void
  danger?: boolean
  children: ReactNode
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={`flex cursor-pointer items-center gap-2 rounded-control px-2.5 py-1.5 text-left text-[12.5px] transition-colors ${
        danger ? 'text-danger hover:bg-danger-soft' : 'text-fg hover:bg-raised'
      }`}
    >
      {children}
    </button>
  )
}

/** Chip is a filter that is either on or off, and says which. */
export function Chip({
  active,
  onClick,
  title,
  children,
}: {
  active: boolean
  onClick: () => void
  title?: string
  children: ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={active}
      className={`inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border px-2.5 text-[13px] transition-colors ${
        active
          ? 'border-accent-line bg-accent-soft font-medium text-accent'
          : 'border-line bg-surface text-muted hover:bg-raised hover:text-fg'
      }`}
    >
      {children}
    </button>
  )
}

/**
 * Segmented is the deck's tab control: one row, one active cell, sliding nothing
 * — the active cell is a raised surface, which reads instantly on both decks.
 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  ariaLabel,
  id,
}: {
  value: T
  onChange: (next: T) => void
  options: Array<{ value: T; label: string; icon?: ReactNode; count?: number }>
  ariaLabel: string
  /** Set when a Field labels the control rather than the control labelling itself. */
  id?: string
}) {
  return (
    <div
      id={id}
      role="group"
      aria-label={ariaLabel}
      className="inline-flex shrink-0 items-center gap-0.5 rounded-control border border-line bg-raised p-0.5"
    >
      {options.map((option) => {
        const active = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
            className={`inline-flex h-7 items-center gap-1.5 rounded-chip px-2.5 text-[13px] transition-colors duration-300 ${
              active
                ? 'bg-surface font-medium text-fg shadow-deck'
                : 'text-muted hover:text-fg'
            }`}
          >
            {option.icon}
            {option.label}
            {option.count === undefined ? null : (
              <span className="font-data text-[11.5px] text-faint">{option.count}</span>
            )}
          </button>
        )
      })}
    </div>
  )
}

export function KeyHint({ children }: { children: ReactNode }) {
  return (
    <kbd className="rounded-chip border border-line bg-raised px-1.5 py-px font-data text-[11px] text-faint">
      {children}
    </kbd>
  )
}

/* ----------------------------------------------------------------- inputs --- */

const CONTROL =
  'w-full rounded-control border border-line bg-surface px-3 text-[13.5px] text-fg transition-colors placeholder:text-faint hover:border-faint/60 focus:border-accent focus:outline-none disabled:cursor-not-allowed disabled:bg-raised disabled:opacity-60'

export function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
}: {
  label: string
  hint?: string
  /** Replaces the hint while the field is invalid — two messages under one
      input is one too many, and the problem outranks the explanation. */
  error?: string
  htmlFor: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="label">
        {label}
      </label>
      {children}
      {error ? (
        <p role="alert" className="text-[12px] leading-snug text-danger">
          {error}
        </p>
      ) : hint ? (
        <p className="text-[12px] leading-snug text-muted">{hint}</p>
      ) : null}
    </div>
  )
}

export function TextInput({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={`${CONTROL} h-9 ${className ?? ''}`} />
}

export function TextArea({
  className,
  prose = false,
  ...rest
}: TextareaHTMLAttributes<HTMLTextAreaElement> & {
  /** Set for words somebody writes — a reason, a note — rather than a manifest. */
  prose?: boolean
}) {
  return (
    <textarea
      {...rest}
      className={`${CONTROL} resize-y py-2 leading-relaxed ${prose ? 'text-[13.5px]' : 'font-mono text-[12.5px]'} ${className ?? ''}`}
    />
  )
}

export function Select({
  className,
  children,
  size = 'md',
  ...rest
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, 'size'> & { size?: 'sm' | 'md' }) {
  return (
    <div className="relative inline-flex min-w-0 w-full items-center">
      <select
        {...rest}
        className={`${CONTROL} cursor-pointer appearance-none pr-8 ${
          size === 'sm' ? 'h-8 text-[13px]' : 'h-9'
        } ${className ?? ''}`}
      >
        {children}
      </select>
      <ChevronDown
        aria-hidden="true"
        className="pointer-events-none absolute right-2.5 size-3.5 text-faint"
      />
    </div>
  )
}

/** SearchInput is the filter that sits in a panel header. */
export function SearchInput({
  value,
  onChange,
  placeholder,
  label,
  className,
}: {
  value: string
  onChange: (next: string) => void
  placeholder: string
  label: string
  className?: string
}) {
  return (
    <div className={`relative ${className ?? 'w-full sm:w-64'}`}>
      <Search
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-faint"
      />
      <input
        type="search"
        value={value}
        aria-label={label}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        className={`${CONTROL} h-9 pl-8 text-[13px]`}
      />
    </div>
  )
}

/* --------------------------------------------------------------- surfaces --- */

/**
 * Panel is the standard surface: a 16px title (with a quiet pill beside it
 * where the panel is one of a set), an optional 13px description, actions on
 * the right, content below.
 */
export function Panel({
  title,
  eyebrow,
  description,
  actions,
  children,
  className,
  bodyClassName,
}: {
  title: string
  eyebrow?: string
  description?: string
  actions?: ReactNode
  children?: ReactNode
  className?: string
  /** Set when the body needs padding; tables and lists sit flush by default. */
  bodyClassName?: string
}) {
  return (
    <section className={`card overflow-hidden ${className ?? ''}`}>
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-line-soft px-5 pt-4 pb-3.5">
        <div className="min-w-0">
          {/* Where the panel is one of a set — a step, a section — the set is
              named in a quiet pill beside the title rather than above it. */}
          <div className="flex min-w-0 items-center gap-2">
            <h2 className="truncate text-[16px] font-bold text-fg">{title}</h2>
            {eyebrow ? (
              <span className="shrink-0 rounded-full border border-line px-2 py-px text-[11.5px] font-medium text-muted">
                {eyebrow}
              </span>
            ) : null}
          </div>
          {description ? (
            <p className="mt-0.5 max-w-2xl text-[13px] leading-relaxed text-muted">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
      </header>
      {children ? <div className={bodyClassName}>{children}</div> : null}
    </section>
  )
}

/*
 * A StatTile is one figure with its glyph: a label, the reading, and an
 * optional line under it. A tile that is reporting a state takes that state's
 * soft fill, fading into the surface; a neutral one stays on the raised tone,
 * so colour on a row of tiles only ever means "this one".
 */
type StatTone = 'neutral' | 'ok' | 'warn' | 'danger'

const STAT_TINT: Record<StatTone, string> = {
  neutral: 'from-raised',
  ok: 'from-ok-soft',
  warn: 'from-warn-soft',
  danger: 'from-danger-soft',
}

const STAT_GLYPH: Record<StatTone, string> = {
  neutral: 'text-muted',
  ok: 'text-ok',
  warn: 'text-warn',
  danger: 'text-danger',
}

const STAT_VALUE: Record<StatTone, string> = {
  neutral: 'text-fg',
  ok: 'text-fg',
  warn: 'text-warn',
  danger: 'text-danger',
}

export function StatTile({
  icon: Icon,
  label,
  value,
  sub,
  tone = 'neutral',
  dim = false,
  data = true,
  to,
  onClick,
  pressed,
  disabled,
  title,
  children,
}: {
  icon: LucideIcon
  label: string
  value: ReactNode
  sub?: ReactNode
  tone?: StatTone
  /** The reading is not a reading yet — failed or in flight — and looks it. */
  dim?: boolean
  /** Figures and identifiers are set as data; a reading that is a phrase is not. */
  data?: boolean
  /** Makes the whole tile a link onto the thing it counts. */
  to?: string
  /** Makes the whole tile a toggle — a filter over the list under it. */
  onClick?: () => void
  pressed?: boolean
  disabled?: boolean
  title?: string
  /** Under the reading: a bar, a breakdown — anything the figure summarises. */
  children?: ReactNode
}) {
  const interactive = Boolean(to || onClick)
  const body = (
    <>
      <span
        className={`mb-4 grid size-10 place-items-center rounded-full border border-line-soft bg-surface shadow-deck ${STAT_GLYPH[tone]}`}
      >
        <Icon aria-hidden="true" className="size-4.5" />
      </span>
      <span
        className={`text-[14px] text-fg ${interactive ? 'transition-colors duration-300 group-hover:text-accent' : ''}`}
      >
        {label}
      </span>
      <span
        className={`mt-1 min-w-0 leading-tight font-bold break-words ${
          data ? 'font-data text-[22px] tabular-nums' : 'text-[18px]'
        } ${dim ? 'text-muted' : STAT_VALUE[tone]}`}
      >
        {value}
      </span>
      {sub ? <span className="mt-1 min-w-0 truncate text-[12.5px] text-muted">{sub}</span> : null}
      {children ? <span className="mt-3 flex min-w-0 flex-col gap-2">{children}</span> : null}
    </>
  )
  const tint = pressed ? 'from-accent-soft border-accent-line' : `${STAT_TINT[tone]} border-line`
  const frame = `flex min-w-0 flex-col rounded-card border bg-linear-to-b ${tint} to-surface p-4 text-left shadow-deck`
  const hover = 'group transition-colors duration-300 enabled:hover:border-faint/60 [&:not(button)]:hover:border-faint/60'

  if (to) {
    return (
      <Link to={to} title={title} className={`${frame} ${hover}`}>
        {body}
      </Link>
    )
  }
  if (onClick) {
    return (
      <button
        type="button"
        title={title}
        aria-pressed={pressed}
        disabled={disabled}
        onClick={onClick}
        className={`${frame} ${hover} disabled:cursor-default disabled:opacity-60`}
      >
        {body}
      </button>
    )
  }
  return (
    <div title={title} className={frame}>
      {body}
    </div>
  )
}

/**
 * Disclosure folds a card's own explanation behind one line — the "why this
 * is like this" prose that belongs in the console rather than only in the
 * manual, kept out of the way of the readings it explains instead of cut.
 * The summary names what is inside; a bare chevron is not enough, since a
 * disclosure that could be anything is one nobody opens on purpose.
 *
 * The content stays in the DOM whichever way it is drawn — a closed
 * `<details>` hides it without unmounting it, so it is still there for
 * find-in-page and a screen reader, and reopening it costs no re-render of
 * whatever is inside.
 *
 * This component draws the disclosure; it does not remember whether it was
 * left open. `open`/`onOpenChange` are the caller's — see
 * `lib/disclosures.ts` for the per-user memory every caller wires in the
 * same way, and note it takes no transition: opening one is instant, the
 * same rule that keeps everything on this deck still except a genuinely
 * open stream.
 */
export function Disclosure({
  summary,
  open,
  onOpenChange,
  children,
  className,
}: {
  /** What is folded away, named — never a bare chevron. */
  summary: ReactNode
  open: boolean
  onOpenChange: (open: boolean) => void
  children: ReactNode
  className?: string
}) {
  return (
    <details
      className={`group card overflow-hidden ${className ?? ''}`}
      open={open}
      onToggle={(event) => onOpenChange(event.currentTarget.open)}
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-5 py-3.5 text-[13px] font-medium text-muted transition-colors duration-300 hover:text-fg [&::-webkit-details-marker]:hidden">
        <ChevronDown aria-hidden="true" className="size-4 shrink-0 group-open:rotate-180" />
        {summary}
      </summary>
      <div className="border-t border-line-soft px-5 py-4">{children}</div>
    </details>
  )
}

/** SectionHeading separates bands of content that are not panels themselves. */
export function SectionHeading({
  title,
  meta,
  children,
}: {
  title: string
  meta?: ReactNode
  children?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <h2 className="text-[16px] font-bold text-fg">{title}</h2>
      {children}
      <span aria-hidden="true" className="h-px min-w-6 flex-1 bg-line" />
      {meta ? <span className="text-[12.5px] text-muted">{meta}</span> : null}
    </div>
  )
}

const NOTICE_TONE = {
  error: 'border-danger/35 bg-danger-soft text-danger',
  warn: 'border-warn/35 bg-warn-soft text-warn',
  info: 'border-line-soft bg-raised/60 text-muted',
  ok: 'border-ok/35 bg-ok-soft text-ok',
}

// A notice leads with the glyph for its tone, in a round chip like every other
// glyph on the deck, so the kind of message is read before the message is.
const NOTICE_GLYPH: Record<keyof typeof NOTICE_TONE, { icon: LucideIcon; chip: string }> = {
  error: { icon: CircleAlert, chip: 'border-danger/35 text-danger' },
  warn: { icon: TriangleAlert, chip: 'border-warn/35 text-warn' },
  info: { icon: Info, chip: 'border-line-soft text-accent' },
  ok: { icon: CircleCheck, chip: 'border-ok/35 text-ok' },
}

export function Notice({
  tone,
  children,
}: {
  tone: keyof typeof NOTICE_TONE
  children: ReactNode
}) {
  const { icon: Icon, chip } = NOTICE_GLYPH[tone]
  return (
    <div
      role={tone === 'error' ? 'alert' : 'status'}
      className={`flex items-start gap-3 rounded-card border px-3.5 py-3 text-[13px] leading-relaxed ${NOTICE_TONE[tone]}`}
    >
      <span
        aria-hidden="true"
        className={`grid size-6 shrink-0 place-items-center rounded-full border bg-surface ${chip}`}
      >
        <Icon className="size-3.5" />
      </span>
      <div className="min-w-0 flex-1 self-center">{children}</div>
    </div>
  )
}

/** EmptyState says what is missing and what to do about it. */
export function EmptyState({
  icon,
  title,
  children,
  action,
}: {
  icon?: ReactNode
  title: string
  children?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center px-6 py-14 text-center">
      {icon ? (
        <span className="mb-4 grid size-12 place-items-center rounded-full border border-line-soft bg-surface text-muted shadow-deck">
          {icon}
        </span>
      ) : null}
      <h3 className="text-[16px] font-bold text-fg">{title}</h3>
      {children ? (
        <p className="mt-1.5 max-w-md text-[13px] leading-relaxed text-muted">{children}</p>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  )
}

/* ----------------------------------------------------------------- tables --- */

const MIN_COLUMN_WIDTH = 56

type ColumnWidths = Record<string, number>

/**
 * ColumnResizeContext is what `Th`/`SortTh` read to know whether they carry a
 * drag handle at all — a table with no `resizeKey` renders exactly as before,
 * so the feature costs nothing where nobody asked for it.
 */
const ColumnResizeContext = createContext<{
  widths: ColumnWidths
  setWidth: (key: string, px: number) => void
} | null>(null)

function loadColumnWidths(storageKey: string): ColumnWidths {
  try {
    const raw = window.localStorage.getItem(storageKey)
    if (!raw) return {}
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object') return {}
    return Object.fromEntries(
      Object.entries(parsed as Record<string, unknown>).filter(
        (entry): entry is [string, number] => typeof entry[1] === 'number',
      ),
    )
  } catch {
    return {}
  }
}

/**
 * Table wraps its rows in a resize context when `resizeKey` is given, so every
 * `columnKey`-carrying `Th`/`SortTh` inside grows a drag handle on its right
 * edge — an operator's own column widths, AWS-console style, rather than the
 * fixed layout everyone is stuck with. `resizeKey` is also the localStorage
 * key: a resize made once is a preference for that table kind, not a
 * one-render adjustment, so it survives a reload.
 */
export function Table({
  children,
  className,
  resizeKey,
}: {
  children: ReactNode
  className?: string
  /** A key unique to this table's column set. Omit to skip resizing entirely. */
  resizeKey?: string
}) {
  const [widths, setWidths] = useState<ColumnWidths>(() =>
    resizeKey ? loadColumnWidths(resizeKey) : {},
  )

  const setWidth = useCallback(
    (key: string, px: number) => {
      setWidths((current) => {
        if (current[key] === px) return current
        const next = { ...current, [key]: px }
        if (resizeKey) {
          try {
            window.localStorage.setItem(resizeKey, JSON.stringify(next))
          } catch {
            // A full or disabled store still leaves the resize working for this session.
          }
        }
        return next
      })
    },
    [resizeKey],
  )

  const table = (
    <table className={`w-full table-fixed border-collapse text-[13.5px] ${className ?? ''}`}>
      {children}
    </table>
  )

  /*
   * A scroll container is what a sticky heading pins against, and this wrapper
   * was one on every table: `overflow-x-auto` forces the other axis to `auto`
   * too, so `Th`'s `sticky` was resolved against a box that never scrolls
   * vertically — inert, on all of them, since the day it was written.
   *
   * The wrapper is only *needed* when the table is wider than the space, and a
   * `w-full table-fixed` table is never wider than the space on its own: it
   * hides columns by breakpoint instead. The one thing that can make it wider is
   * the reader dragging a column past what is there. So the scroll container is
   * grown at that moment and not before, which leaves the ordinary case with the
   * page as its scrollport. A resized table trades the pinned heading for the
   * ability to reach the column it just widened; that is the reader's own doing
   * and reversible by dragging back — and it puts `--table-sticky-top` back to 0
   * on the way, because an offset resolved against a box that does not scroll
   * vertically does not pin a heading, it *pushes it down* by that many pixels
   * and leaves a blank band where it used to be.
   */
  const resized = Object.keys(widths).length > 0

  return (
    <div className={`min-w-0 ${resized ? 'overflow-x-auto [--table-heading-position:relative]' : ''}`}>
      {resizeKey ? (
        <ColumnResizeContext.Provider value={{ widths, setWidth }}>
          {table}
        </ColumnResizeContext.Provider>
      ) : (
        table
      )}
    </div>
  )
}

/**
 * ResizeHandle is the draggable divider at a column's right edge. It reads the
 * starting width off the live `<th>` rather than off state, so a column that
 * has never been dragged — still sized by its Tailwind class — resizes from
 * whatever it is actually rendering at, not from an unset value.
 */
function ResizeHandle({ columnKey }: { columnKey: string }) {
  const ctx = useContext(ColumnResizeContext)
  if (!ctx) return null

  function onPointerDown(event: ReactPointerEvent<HTMLSpanElement>) {
    event.preventDefault()
    event.stopPropagation()
    const cell = event.currentTarget.closest('th')
    const startWidth = cell?.getBoundingClientRect().width ?? MIN_COLUMN_WIDTH
    const startX = event.clientX

    function onMove(moveEvent: PointerEvent) {
      ctx?.setWidth(columnKey, Math.max(MIN_COLUMN_WIDTH, startWidth + (moveEvent.clientX - startX)))
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  return (
    <span
      role="separator"
      aria-orientation="vertical"
      onPointerDown={onPointerDown}
      onClick={(event) => event.stopPropagation()}
      className="absolute inset-y-0 -right-0.5 z-10 w-1.5 cursor-col-resize touch-none select-none rounded-full hover:bg-accent/50 active:bg-accent/70"
    />
  )
}

/** The inline width a resized column carries, and the handle beside it. */
function useColumnResize(columnKey?: string) {
  const ctx = useContext(ColumnResizeContext)
  const width = columnKey ? ctx?.widths[columnKey] : undefined
  return {
    style: width ? { width: `${width}px` } : undefined,
    handle: ctx && columnKey ? <ResizeHandle columnKey={columnKey} /> : null,
  }
}

/**
 * A column heading reads as the row it is: semibold in the interface face, in
 * the foreground tone, on a raised band — not the quiet `label` a field name
 * wears, which left the heading row looking like one more row of data.
 */
const HEADING = 'text-[12.5px] font-semibold whitespace-nowrap text-fg'

/**
 * Th is a column heading, and where it sits vertically is not its own decision:
 * `--table-heading-position` and `--table-sticky-top` are read off whatever box
 * this table is in. At rest they are `relative` and `0`, which is a heading
 * exactly where it falls; a surface that has established the *window* is what
 * scrolls it — the audit trail — sets them to `sticky` and the page header's
 * height. Carrying `sticky` here instead was tried and is the reason a heading
 * turned up 57px below its own row on every table in the console: most of them
 * sit inside a card that is `overflow-hidden` for its corners, which is a scroll
 * container, and an offset resolved against one of those is a push rather than
 * a pin.
 */
export function Th({
  children,
  className,
  align = 'left',
  columnKey,
}: {
  children?: ReactNode
  className?: string
  align?: 'left' | 'right'
  /** Set to grow a drag handle, in a table whose `Table` carries a `resizeKey`. */
  columnKey?: string
}) {
  const { style, handle } = useColumnResize(columnKey)
  return (
    <th
      scope="col"
      style={style}
      className={`${HEADING} [position:var(--table-heading-position)] top-[var(--table-sticky-top)] z-1 bg-raised shadow-[inset_0_-1px_0_var(--color-line)] px-5 py-2.5 ${
        align === 'right' ? 'text-right' : 'text-left'
      } ${className ?? ''}`}
    >
      {children}
      {handle}
    </th>
  )
}

/** Which way a sorted column is ordered, or `null` for a column nobody sorted by. */
export type SortDirection = 'asc' | 'desc' | null

/**
 * SortTh is a heading that sorts its column. The control is a button *inside*
 * the cell rather than a click handler on the cell, so it is reachable by
 * keyboard and announced as something that does anything; `aria-sort` goes on
 * the cell itself, which is where a screen reader looks for it.
 *
 * An unsorted column shows its arrows faintly on hover only — a list of eight
 * headings each with a permanent glyph reads as decoration, and the point of the
 * affordance is to be found when it is wanted.
 */
export function SortTh({
  children,
  className,
  align = 'left',
  direction,
  onSort,
  columnKey,
}: {
  children?: ReactNode
  className?: string
  align?: 'left' | 'right'
  direction: SortDirection
  onSort: () => void
  /** Set to grow a drag handle, in a table whose `Table` carries a `resizeKey`. */
  columnKey?: string
}) {
  const { style, handle } = useColumnResize(columnKey)
  const arrow =
    direction === 'asc' ? (
      <ChevronUp aria-hidden="true" className="size-3 text-accent" />
    ) : direction === 'desc' ? (
      <ChevronDown aria-hidden="true" className="size-3 text-accent" />
    ) : (
      <ChevronsUpDown
        aria-hidden="true"
        className="size-3 text-faint opacity-0 transition-opacity group-hover:opacity-100"
      />
    )

  return (
    <th
      scope="col"
      aria-sort={direction === 'asc' ? 'ascending' : direction === 'desc' ? 'descending' : 'none'}
      style={style}
      className={`${HEADING} [position:var(--table-heading-position)] top-[var(--table-sticky-top)] z-1 bg-raised shadow-[inset_0_-1px_0_var(--color-line)] px-5 py-2.5 ${
        align === 'right' ? 'text-right' : 'text-left'
      } ${className ?? ''}`}
    >
      <button
        type="button"
        onClick={onSort}
        className={`group ${HEADING} flex w-full cursor-pointer items-center gap-1 transition-colors hover:text-accent ${
          align === 'right' ? 'justify-end' : ''
        }`}
      >
        <span className="min-w-0 truncate">{children}</span>
        {arrow}
      </button>
      {handle}
    </th>
  )
}

/**
 * Row is the standard table row: a hairline above, and a hover that lifts the
 * whole row rather than only the glyph the pointer happens to be over.
 *
 * It carries `group/row` so the row's own name can answer for a hover anywhere
 * along it — a name is a small target in a wide row, and asking an operator to
 * land on the text itself is what made the lists read as static. `focus-within`
 * repeats the same lift for the keyboard, so tabbing down a list shows the same
 * row the pointer would.
 */
export function Row({
  children,
  className,
  title,
  onOpen,
}: {
  children: ReactNode
  className?: string
  title?: string
  /**
   * Opens whatever this row is a row of. It is a convenience on top of a real
   * control inside the row, never the only way in: a `<tr>` cannot be focused
   * or announced as something that does anything, so a table whose rows only
   * opened by click would be a table nobody could read with a keyboard. The
   * cell that carries the trigger is the accessible path; this is the large
   * target for a mouse.
   */
  onOpen?: () => void
}) {
  return (
    <tr
      title={title}
      onClick={
        onOpen
          ? (event) => {
              // A row full of buttons — replay, diff, a row menu — must not
              // also open the row when one of them is clicked, and selecting
              // text out of a cell is not a click on the row either.
              const target = event.target as HTMLElement
              if (target.closest('button, a, input, select, textarea')) return
              if (window.getSelection()?.toString()) return
              onOpen()
            }
          : undefined
      }
      className={`group/row border-t border-line-soft transition-colors even:bg-raised/40 hover:bg-raised focus-within:bg-raised ${
        onOpen ? 'cursor-pointer' : ''
      } ${className ?? ''}`}
    >
      {children}
    </tr>
  )
}

/**
 * OBJECT_NAME is how a name that addresses something is set: the row's first
 * column, semibold in the foreground tone, and the accent when the row is under
 * the pointer or holds focus. The name is the one string in a list whose length
 * nobody controls — a pod carries its ReplicaSet hash and its own suffix — so it
 * wraps rather than truncating, and it wears no underline, which a wrap would
 * cut in half.
 *
 * It used to sit beside a hairline lime bar on the row's edge that said "this
 * opens". The bar went with the list refresh: thirty of them down a list read
 * as a stripe rather than a signal, and the weight now says the same thing — a
 * name that opens something is semibold, a name that only holds a value is not.
 */
export const OBJECT_NAME =
  'block min-w-0 cursor-pointer text-left font-data font-semibold text-fg [overflow-wrap:anywhere] transition-colors group-hover/row:text-accent hover:text-accent focus-visible:text-accent'

export function Td({
  children,
  className,
  title,
}: {
  children?: ReactNode
  className?: string
  title?: string
}) {
  return (
    <td title={title} className={`px-5 py-3 ${className ?? ''}`}>
      {children}
    </td>
  )
}

/* ---------------------------------------------------------------- overlays --- */

/**
 * Sheet is the deck's editing surface: a panel that slides in from the right
 * over a scrim, closed with Escape or a click outside. Forms live in the body,
 * actions in the footer. Every editing surface uses this — there is no second
 * dialog pattern.
 */
/**
 * How wide a sheet opens. The narrow two are for editing a handful of fields;
 * the wide ones are for reading, where the content is a table or a manifest and
 * the constraint is the line, not the form.
 *
 * `wide` is a viewport fraction rather than a pixel figure on purpose: it is for
 * a surface with several tabs of dense content, where on a large display the
 * right width is "most of the screen" and on a small one the max-width never
 * binds anyway — the sheet is already full width below its own breakpoint.
 *
 * The classes are a lookup rather than an interpolation because Tailwind reads
 * the source for literal class names; a template string would compile to a rule
 * that does not exist.
 */
export type SheetWidth = 'md' | 'lg' | 'xl' | '2xl' | 'wide'

const SHEET_WIDTH: Record<SheetWidth, string> = {
  md: 'max-w-[520px]',
  lg: 'max-w-[680px]',
  xl: 'max-w-[900px]',
  '2xl': 'max-w-[1100px]',
  wide: 'max-w-[85vw]',
}

/** Which sheets and dialogs are open, oldest first. Only the last one answers. */
const SHEET_STACK: object[] = []

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/*
 * What every overlay owes the keyboard: Escape closes the topmost one only,
 * focus moves into it when it opens (unless something inside already took it,
 * as an `autoFocus` does), Tab and Shift+Tab stay inside it while it is the
 * topmost, and focus goes back to whatever opened it when it closes.
 */
function useOverlay(panel: React.RefObject<HTMLElement | null>, onClose: () => void) {
  // Escape must call the current onClose, not the one the overlay opened with.
  const latest = useRef(onClose)
  useEffect(() => {
    latest.current = onClose
  })

  // Opened once: a new onClose identity must not re-run the focus move.
  useEffect(() => {
    const token = {}
    SHEET_STACK.push(token)
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const node = panel.current
    if (node && !node.contains(document.activeElement)) {
      const first = node.querySelector<HTMLElement>(`[data-autofocus], ${FOCUSABLE}`)
      ;(first ?? node).focus()
    }

    function onKey(event: KeyboardEvent) {
      if (SHEET_STACK[SHEET_STACK.length - 1] !== token) return
      if (event.key === 'Escape') {
        latest.current()
        return
      }
      if (event.key !== 'Tab' || !panel.current) return
      const items = Array.from(panel.current.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (item) => item.offsetParent !== null || item === document.activeElement,
      )
      if (items.length === 0) return
      const first = items[0]
      const last = items[items.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => {
      const at = SHEET_STACK.indexOf(token)
      if (at >= 0) SHEET_STACK.splice(at, 1)
      window.removeEventListener('keydown', onKey)
      if (opener && opener.isConnected) opener.focus()
    }
  }, [panel])
}

export function Sheet({
  title,
  eyebrow,
  onClose,
  children,
  footer,
  onSubmit,
  width = 'md',
}: {
  title: ReactNode
  eyebrow?: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  /** When given, the body is wrapped in a form so Enter submits. */
  onSubmit?: (event: React.FormEvent<HTMLFormElement>) => void
  width?: SheetWidth
}) {
  const titleId = useId()
  const panel = useRef<HTMLDivElement | null>(null)

  /*
   * Escape closes the **topmost** sheet and only that one.
   *
   * One sheet at a time used to be the whole rule — a workload action and a
   * release's values are panels inside the detail drawer rather than surfaces
   * over it. A confirmation broke it: the question asked before a destructive
   * act opens over the sheet that asked. With every instance listening on the
   * window, one Escape reached both — and in the detail drawer, whose own
   * Escape *is* what asks the question, it re-asked it forever instead of
   * answering it.
   *
   * The stack is module-level rather than a context because it is about paint
   * order, which no provider knows: what should answer is whatever was mounted
   * last. See `useOverlay`, which also keeps focus inside the topmost one.
   */
  useOverlay(panel, onClose)

  const body = (
    <>
      {/* The sheet's body is its own scrollport, so a table in here pins at its
          top rather than under the page header it cannot see. */}
      <div className="flex flex-1 flex-col gap-4 overflow-y-auto overscroll-contain px-5 py-4 [--table-heading-position:relative]">
        {children}
      </div>
      {footer ? (
        <footer className="flex shrink-0 items-center justify-end gap-2 border-t border-line-soft bg-raised/40 px-5 py-3.5">
          {footer}
        </footer>
      ) : null}
    </>
  )

  return (
    <div className="fixed inset-0 z-40 flex justify-end">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        tabIndex={-1}
        className="scrim-in absolute inset-0 bg-scrim backdrop-blur-[2px]"
      />

      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={`sheet-in relative my-2 mr-2 flex h-[calc(100%-1rem)] w-full flex-col overflow-hidden rounded-card border border-line bg-surface shadow-lift outline-none max-sm:m-0 max-sm:h-full max-sm:rounded-none ${SHEET_WIDTH[width]}`}
      >
        <header className="flex shrink-0 items-start justify-between gap-3 border-b border-line-soft px-5 pt-4 pb-3.5">
          <div className="min-w-0">
            <h2 id={titleId} className="truncate text-[18px] font-bold text-fg">
              {title}
            </h2>
            {/* What the sheet is about, said under its title rather than as a
                micro-label above it. */}
            {eyebrow ? (
              <p className="mt-0.5 truncate text-[13px] text-muted">{eyebrow}</p>
            ) : null}
          </div>
          <IconButton label="Close" onClick={onClose} type="button" className="rounded-full">
            <X aria-hidden="true" className="size-4" />
          </IconButton>
        </header>

        {onSubmit ? (
          <form onSubmit={onSubmit} className="flex min-h-0 flex-1 flex-col">
            {body}
          </form>
        ) : (
          <div className="flex min-h-0 flex-1 flex-col">{body}</div>
        )}
      </div>
    </div>
  )
}

/**
 * Dialog is a question, centred: a title, a few lines, two answers. It is for
 * the confirmation asked before an act, which is two buttons and not an
 * editing surface — every editing surface is still a `Sheet`. It shares the
 * sheet's overlay stack, so Escape and focus behave the same over either.
 */
export function Dialog({
  title,
  subject,
  onClose,
  children,
  footer,
  tone = 'default',
}: {
  title: ReactNode
  /** What the question is about — a cluster, an object — under the title. */
  subject?: string
  onClose: () => void
  children: ReactNode
  footer: ReactNode
  /** A destructive question is an alert dialog. */
  tone?: 'default' | 'danger'
}) {
  const titleId = useId()
  const bodyId = useId()
  const panel = useRef<HTMLDivElement | null>(null)
  useOverlay(panel, onClose)

  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-4">
      <button
        type="button"
        aria-label="Close"
        tabIndex={-1}
        onClick={onClose}
        className="scrim-in absolute inset-0 bg-scrim backdrop-blur-[2px]"
      />
      <div
        ref={panel}
        role={tone === 'danger' ? 'alertdialog' : 'dialog'}
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
        tabIndex={-1}
        className="pop-in relative flex w-full max-w-[460px] flex-col rounded-card border border-line bg-surface shadow-lift outline-none"
      >
        <div className="px-6 pt-5 pb-4">
          <h2 id={titleId} className="text-[18px] font-bold text-fg">
            {title}
          </h2>
          {subject ? (
            <p className="mt-0.5 truncate font-data text-[13px] text-muted">{subject}</p>
          ) : null}
          <div id={bodyId} className="mt-3 text-[13.5px] leading-relaxed text-muted">
            {children}
          </div>
        </div>
        <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-line-soft px-6 py-4">
          {footer}
        </footer>
      </div>
    </div>
  )
}

/**
 * CodeBlock is the copy surface: mono on a sunken slab, with copy where the eye
 * already is. Install commands and tokens are meant to be taken away, so copying
 * is the primary affordance rather than an afterthought.
 */
export function CodeBlock({
  value,
  label,
  secret = false,
  wrap = false,
}: {
  value: string
  label?: string
  /** Masks the value until revealed. For tokens, which shoulder-surf badly. */
  secret?: boolean
  wrap?: boolean
}) {
  const [copied, setCopied] = useState(false)
  const [revealed, setRevealed] = useState(!secret)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard access is denied outside a secure context; the value is
      // selectable on screen either way, so there is nothing to report.
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      {label ? <span className="label">{label}</span> : null}
      <div className="flex items-start gap-1 rounded-control border border-line bg-sunken p-1 pl-3">
        <pre
          className={`min-w-0 flex-1 overflow-x-auto py-1.5 font-mono text-[12.5px] leading-relaxed text-fg ${
            wrap ? 'whitespace-pre-wrap' : 'whitespace-pre'
          }`}
        >
          {revealed ? value : '•'.repeat(Math.min(value.length, 48))}
        </pre>
        <div className="flex shrink-0 items-center gap-0.5">
          {secret ? (
            <IconButton
              type="button"
              label={revealed ? 'Hide value' : 'Reveal value'}
              onClick={() => setRevealed((current) => !current)}
            >
              {revealed ? (
                <EyeOff aria-hidden="true" className="size-3.5" />
              ) : (
                <Eye aria-hidden="true" className="size-3.5" />
              )}
            </IconButton>
          ) : null}
          <IconButton type="button" label={copied ? 'Copied' : 'Copy'} onClick={copy}>
            {copied ? (
              <Check aria-hidden="true" className="size-3.5 text-ok" />
            ) : (
              <Copy aria-hidden="true" className="size-3.5" />
            )}
          </IconButton>
        </div>
      </div>
    </div>
  )
}

/** Slab is a read-only block of machine output: logs, manifests, kubeconfigs. */
export function Slab({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <pre
      className={`overflow-auto rounded-control border border-line bg-sunken px-3 py-2.5 font-mono text-[12px] leading-relaxed text-fg ${className ?? ''}`}
    >
      {children}
    </pre>
  )
}

/**
 * Meter is the deck's utilisation bar: a label, the reading set as data, and a
 * track. It is a bar rather than a chart because the metrics behind it are a
 * single live sample, not a series — metrics-server keeps a couple of minutes
 * and nothing more, so there is no history to plot and none is implied.
 *
 * A meter with no capacity to measure against still renders the value and says
 * the denominator is unknown, since "using 40m, no limit set" is exactly the
 * thing an operator wants to notice.
 */
const PERCENT = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 })
const PERCENT_FINE = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 })

/** A 0–100 reading as a percentage, one decimal below ten so a small one still moves. */
function formatPercent(percent: number): string {
  return (percent < 10 ? PERCENT_FINE : PERCENT).format(percent / 100)
}

export function Meter({
  label,
  value,
  percent,
  capacity,
  className,
}: {
  label: string
  /** The reading itself, already formatted. */
  value: string
  /** Utilisation 0-100, or undefined when nothing bounds it. */
  percent?: number
  /** The denominator, already formatted. Omitted when there is none. */
  capacity?: string
  className?: string
}) {
  const bounded = percent !== undefined && capacity !== undefined
  const tone = bounded ? usageTone(percent) : 'idle'
  const fill = bounded ? Math.min(100, Math.max(0, percent)) : 0

  return (
    <div className={`min-w-0 ${className ?? ''}`}>
      <div className="flex items-baseline gap-2">
        <span className="text-[12.5px] text-muted">{label}</span>
        {bounded ? (
          <span className="ml-auto font-data text-[13px] font-semibold text-fg tabular-nums">
            {formatPercent(percent)}
          </span>
        ) : null}
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuenow={bounded ? Math.round(percent) : undefined}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={bounded ? `${value} of ${capacity}` : value}
        className="mt-1.5 h-2 overflow-hidden rounded-full bg-raised"
      >
        {/* An unbounded reading gets a hatch rather than a fill: a full-width
            bar would read as "at capacity", which is the opposite of unknown. */}
        {bounded ? (
          <span
            aria-hidden="true"
            className={`block h-full rounded-full ${TONE_FILL[tone]}`}
            style={{ width: `${fill}%` }}
          />
        ) : (
          <span
            aria-hidden="true"
            className="block h-full w-full opacity-30"
            style={{
              backgroundImage:
                'repeating-linear-gradient(135deg, currentColor 0 2px, transparent 2px 6px)',
            }}
          />
        )}
      </div>
      <p className="mt-1.5 flex items-baseline gap-1.5 text-[12px] text-faint">
        <span className="font-data text-fg tabular-nums">{value}</span>
        <span className="font-data tabular-nums">{bounded ? `/ ${capacity}` : 'no limit'}</span>
      </p>
    </div>
  )
}

/**
 * MiniMeter is one reading on one line: what it is, how full it is, and the
 * number. It is `Meter` with the label block folded into the row, for a table
 * cell where the surrounding columns already carry the units and the point is
 * the shape of the column rather than any one row's figure.
 *
 * It keeps `Meter`'s tones and the same `usageTone` thresholds on purpose — two
 * readings of the same thing on two pages must not disagree about what counts
 * as hot — and it keeps the hatch for an unbounded reading for the same reason
 * a full bar would be wrong there: it reads as "at capacity", the opposite of
 * unknown. It is deliberately not a variant flag on `Meter`: the two differ in
 * every line of their layout and nothing in either is shared but the tone.
 */
export function MiniMeter({
  label,
  percent,
  title,
  className,
}: {
  label: string
  /** Utilisation 0-100, or undefined when nothing bounds it. */
  percent?: number
  /** The full reading, for the cell's tooltip — `390m / 4.00 cores`. */
  title?: string
  className?: string
}) {
  const bounded = percent !== undefined
  const tone = bounded ? usageTone(percent) : 'idle'
  const fill = bounded ? Math.min(100, Math.max(0, percent)) : 0

  return (
    <div
      title={title}
      className={`grid grid-cols-[30px_minmax(0,1fr)_36px] items-center gap-x-2 ${className ?? ''}`}
    >
      <span className="text-[11.5px] font-medium text-faint">{label}</span>
      <span
        role="meter"
        aria-label={label}
        aria-valuenow={bounded ? Math.round(percent) : undefined}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={title ?? (bounded ? `${Math.round(percent)}%` : 'not reported')}
        className="h-1.5 overflow-hidden rounded-full bg-raised"
      >
        {bounded ? (
          <span
            aria-hidden="true"
            className={`block h-full rounded-full ${TONE_FILL[tone]}`}
            style={{ width: `${fill}%` }}
          />
        ) : (
          <span
            aria-hidden="true"
            className="block h-full w-full opacity-30"
            style={{
              backgroundImage:
                'repeating-linear-gradient(135deg, currentColor 0 2px, transparent 2px 6px)',
            }}
          />
        )}
      </span>
      <span className="text-right font-data text-[11.5px] font-semibold text-fg tabular-nums">
        {bounded ? formatPercent(percent) : '—'}
      </span>
    </div>
  )
}

/** DetailList is the label/value grid used in headers, sheets, and summaries. */
export function DetailList({
  rows,
  columns = 1,
}: {
  rows: Array<{ term: string; value: ReactNode; tone?: 'default' | 'warn' | 'bad' }>
  columns?: 1 | 2
}) {
  return (
    <dl
      className={`grid gap-x-6 gap-y-3 ${columns === 2 ? 'sm:grid-cols-2' : ''}`}
    >
      {rows.map((row) => (
        <div key={row.term} className="min-w-0">
          <dt className="label">{row.term}</dt>
          <dd
            className={`mt-0.5 truncate font-data text-[13px] ${
              row.tone === 'bad' ? 'text-danger' : row.tone === 'warn' ? 'text-warn' : 'text-fg'
            }`}
          >
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}
