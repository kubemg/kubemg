import { useEffect, useId, useState } from 'react'
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react'
import { Check, ChevronDown, Copy, Eye, EyeOff, Loader2, Search, X } from 'lucide-react'
import type { Cluster, Environment } from '../api/types'
import { clusterStateLabel, clusterTone } from '../lib/status'
import type { Tone } from '../lib/status'

/* ------------------------------------------------------------------ state --- */

const TONE_CHIP: Record<Tone, string> = {
  ok: 'bg-ok-soft text-ok',
  warn: 'bg-warn-soft text-warn',
  bad: 'bg-danger-soft text-danger',
  idle: 'bg-raised text-muted',
  accent: 'bg-accent-soft text-accent',
}

const TONE_DOT: Record<Tone, string> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-danger',
  idle: 'bg-faint',
  accent: 'bg-accent',
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
      className={`inline-flex items-center gap-1.5 rounded-chip px-2 py-0.5 text-[12px] font-medium whitespace-nowrap ${TONE_CHIP[tone]}`}
    >
      {dot ? (
        <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${TONE_DOT[tone]}`} />
      ) : null}
      {children}
    </span>
  )
}

/** ClusterState renders a cluster's last known connection state. */
export function ClusterState({ cluster }: { cluster: Cluster }) {
  return (
    <Pill tone={clusterTone(cluster)} title={cluster.status_message}>
      {clusterStateLabel(cluster)}
    </Pill>
  )
}

/** ActivityTag renders whether an account may sign in. */
export function ActivityTag({ active }: { active: boolean }) {
  return <Pill tone={active ? 'ok' : 'idle'}>{active ? 'Active' : 'Disabled'}</Pill>
}

const ENVIRONMENT_TAG: Record<Environment, string> = {
  prod: 'border-danger/40 text-danger',
  staging: 'border-warn/40 text-warn',
  dev: 'border-line text-muted',
}

const ENVIRONMENT_DOT: Record<Environment, string> = {
  prod: 'bg-danger',
  staging: 'bg-warn',
  dev: 'bg-faint',
}

export function EnvironmentTag({ environment }: { environment: Environment }) {
  return (
    <span
      className={`inline-flex items-center rounded-chip border px-1.5 py-px font-mono text-[11px] tracking-wide uppercase ${ENVIRONMENT_TAG[environment]}`}
    >
      {environment}
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
  primary: 'bg-accent text-on-accent hover:bg-accent-hover',
  secondary: 'border border-line bg-surface text-fg hover:border-faint/60 hover:bg-raised',
  ghost: 'text-muted hover:bg-raised hover:text-fg',
  danger: 'border border-danger/40 text-danger hover:bg-danger-soft hover:border-danger/70',
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: keyof typeof BUTTON_VARIANT
  size?: keyof typeof BUTTON_SIZE
}

export function Button({
  variant = 'secondary',
  size = 'md',
  className,
  children,
  ...rest
}: ButtonProps) {
  return (
    <button
      {...rest}
      className={`inline-flex shrink-0 items-center justify-center rounded-control font-medium whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-45 ${BUTTON_SIZE[size]} ${BUTTON_VARIANT[variant]} ${className ?? ''}`}
    >
      {children}
    </button>
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
      className={`inline-grid size-8 shrink-0 place-items-center rounded-control text-muted transition-colors disabled:cursor-not-allowed disabled:opacity-40 ${hover} ${className ?? ''}`}
    >
      {children}
      <span className="sr-only">{label}</span>
    </button>
  )
}

/** Chip is a filter that is either on or off, and says which. */
export function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
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
}: {
  value: T
  onChange: (next: T) => void
  options: Array<{ value: T; label: string; icon?: ReactNode; count?: number }>
  ariaLabel: string
}) {
  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      className="inline-flex shrink-0 items-center gap-0.5 rounded-control border border-line bg-raised p-0.5"
    >
      {options.map((option) => {
        const active = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(option.value)}
            className={`inline-flex h-7 items-center gap-1.5 rounded-[6px] px-2.5 text-[13px] transition-colors ${
              active
                ? 'bg-surface font-medium text-fg shadow-deck'
                : 'text-muted hover:text-fg'
            }`}
          >
            {option.icon}
            {option.label}
            {option.count === undefined ? null : (
              <span className="font-mono text-[11.5px] text-faint">{option.count}</span>
            )}
          </button>
        )
      })}
    </div>
  )
}

export function KeyHint({ children }: { children: ReactNode }) {
  return (
    <kbd className="rounded-[5px] border border-line bg-raised px-1.5 py-px font-mono text-[11px] text-faint">
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
  htmlFor,
  children,
}: {
  label: string
  hint?: string
  htmlFor: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="label">
        {label}
      </label>
      {children}
      {hint ? <p className="text-[12px] leading-snug text-muted">{hint}</p> : null}
    </div>
  )
}

export function TextInput({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={`${CONTROL} h-9 ${className ?? ''}`} />
}

export function TextArea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      {...rest}
      className={`${CONTROL} resize-y py-2 font-mono text-[12.5px] leading-relaxed ${className ?? ''}`}
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
        onChange={(event) => onChange(event.target.value)}
        className={`${CONTROL} h-8 pl-8 text-[13px]`}
      />
    </div>
  )
}

/* --------------------------------------------------------------- surfaces --- */

/**
 * Panel is the standard surface: an eyebrow-and-title header, optional
 * description, actions on the right, content below.
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
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-line-soft px-4 py-3">
        <div className="min-w-0">
          {eyebrow ? <p className="label mb-0.5">{eyebrow}</p> : null}
          <h2 className="truncate text-[15px] font-semibold text-fg">{title}</h2>
          {description ? (
            <p className="mt-1 max-w-2xl text-[12.5px] leading-relaxed text-muted">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
      </header>
      {children ? <div className={bodyClassName}>{children}</div> : null}
    </section>
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
      <h2 className="text-[13px] font-semibold tracking-[0.02em] text-fg uppercase">{title}</h2>
      {children}
      <span aria-hidden="true" className="h-px min-w-6 flex-1 bg-line" />
      {meta ? <span className="text-[12.5px] text-muted">{meta}</span> : null}
    </div>
  )
}

const NOTICE_TONE = {
  error: 'border-danger/35 bg-danger-soft text-danger',
  warn: 'border-warn/35 bg-warn-soft text-warn',
  info: 'border-line bg-raised text-muted',
  ok: 'border-ok/35 bg-ok-soft text-ok',
}

export function Notice({
  tone,
  children,
}: {
  tone: keyof typeof NOTICE_TONE
  children: ReactNode
}) {
  return (
    <p
      role={tone === 'error' ? 'alert' : undefined}
      className={`rounded-control border px-3 py-2.5 text-[12.5px] leading-relaxed ${NOTICE_TONE[tone]}`}
    >
      {children}
    </p>
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
        <span className="mb-3 grid size-10 place-items-center rounded-card border border-line bg-raised text-faint">
          {icon}
        </span>
      ) : null}
      <p className="text-[14.5px] font-medium text-fg">{title}</p>
      {children ? (
        <p className="mt-1.5 max-w-md text-[12.5px] leading-relaxed text-muted">{children}</p>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  )
}

/* ----------------------------------------------------------------- tables --- */

export function Table({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className="min-w-0 overflow-x-auto">
      <table className={`w-full table-fixed border-collapse text-[13.5px] ${className ?? ''}`}>
        {children}
      </table>
    </div>
  )
}

export function Th({
  children,
  className,
  align = 'left',
}: {
  children?: ReactNode
  className?: string
  align?: 'left' | 'right'
}) {
  return (
    <th
      scope="col"
      className={`label sticky top-0 z-1 bg-surface px-4 py-2.5 ${
        align === 'right' ? 'text-right' : 'text-left'
      } ${className ?? ''}`}
    >
      {children}
    </th>
  )
}

/** Row is the standard table row: a quiet hover, a hairline below. */
export function Row({
  children,
  className,
  title,
}: {
  children: ReactNode
  className?: string
  title?: string
}) {
  return (
    <tr
      title={title}
      className={`border-t border-line-soft transition-colors hover:bg-raised/70 ${className ?? ''}`}
    >
      {children}
    </tr>
  )
}

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
    <td title={title} className={`px-4 py-2.5 ${className ?? ''}`}>
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
  width?: 'md' | 'lg'
}) {
  const titleId = useId()

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const body = (
    <>
      <div className="flex flex-1 flex-col gap-4 overflow-y-auto p-4">{children}</div>
      {footer ? (
        <footer className="flex shrink-0 items-center justify-end gap-2 border-t border-line-soft bg-raised/40 px-4 py-3">
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
        className="scrim-in absolute inset-0 bg-black/55 backdrop-blur-[2px]"
      />

      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={`sheet-in relative flex h-full w-full flex-col border-l border-line bg-surface lift ${
          width === 'lg' ? 'max-w-[680px]' : 'max-w-[520px]'
        }`}
      >
        <header className="flex min-h-14 shrink-0 items-start justify-between gap-3 border-b border-line-soft px-4 py-3">
          <div className="min-w-0">
            {eyebrow ? <p className="label mb-0.5">{eyebrow}</p> : null}
            <h2 id={titleId} className="truncate text-[15px] font-semibold text-fg">
              {title}
            </h2>
          </div>
          <IconButton label="Close" onClick={onClose} type="button">
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
            className={`mt-0.5 truncate font-mono text-[13px] ${
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
