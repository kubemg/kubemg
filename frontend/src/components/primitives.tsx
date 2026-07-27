import { useEffect, useId, useState } from 'react'
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react'
import { Check, Copy, Eye, EyeOff, X } from 'lucide-react'
import type { Environment } from '../api/types'

const ENVIRONMENT_STYLE: Record<Environment, string> = {
  prod: 'text-danger border-danger/35',
  staging: 'text-warn border-warn/35',
  dev: 'text-muted border-line',
}

const ENVIRONMENT_DOT: Record<Environment, string> = {
  prod: 'bg-danger',
  staging: 'bg-warn',
  dev: 'bg-faint',
}

export function EnvironmentTag({ environment }: { environment: Environment }) {
  return (
    <span
      className={`inline-flex items-center rounded-[3px] border px-1.5 py-px font-mono text-[10px] tracking-wide uppercase ${ENVIRONMENT_STYLE[environment]}`}
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

const PILL_TONE = {
  ok: 'bg-ok-soft text-ok',
  bad: 'bg-danger-soft text-danger',
  warn: 'bg-warn-soft text-warn',
  neutral: 'bg-raised text-muted',
  accent: 'bg-primary-soft text-primary',
} as const

const PILL_DOT = {
  ok: 'bg-ok',
  bad: 'bg-danger',
  warn: 'bg-warn',
  neutral: 'bg-faint',
  accent: 'bg-primary',
} as const

export type PillTone = keyof typeof PILL_TONE

/** Pill is the compact state chip: always a dot plus a word, never colour alone. */
export function Pill({
  tone,
  dot = true,
  children,
  title,
}: {
  tone: PillTone
  dot?: boolean
  children: ReactNode
  title?: string
}) {
  return (
    <span
      title={title}
      className={`inline-flex items-center gap-1.5 rounded-full px-2 py-px text-[11.5px] font-medium ${PILL_TONE[tone]}`}
    >
      {dot ? (
        <span aria-hidden="true" className={`inline-block size-1.5 rounded-full ${PILL_DOT[tone]}`} />
      ) : null}
      {children}
    </span>
  )
}

const STATUS_LABEL: Record<string, string> = {
  healthy: 'Healthy',
  unhealthy: 'Unreachable',
  pending: 'Never checked',
}

/** StatusDot renders a cluster's last known connection state. */
export function StatusDot({ status, message }: { status: string; message?: string }) {
  const tone: PillTone = status === 'healthy' ? 'ok' : status === 'unhealthy' ? 'bad' : 'neutral'
  return (
    <Pill tone={tone} title={message}>
      {STATUS_LABEL[status] ?? status}
    </Pill>
  )
}

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
    <div className="flex flex-col gap-1">
      <label htmlFor={htmlFor} className="label">
        {label}
      </label>
      {children}
      {hint ? <p className="text-[11px] leading-snug text-muted">{hint}</p> : null}
    </div>
  )
}

const CONTROL =
  'w-full rounded-[5px] border border-line bg-surface px-2.5 py-1.5 text-[13px] text-fg placeholder:text-faint transition-colors hover:border-faint focus:border-primary focus:outline-none disabled:bg-raised disabled:opacity-60'

export function TextInput({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={`${CONTROL} ${className ?? ''}`} />
}

export function TextArea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      {...rest}
      className={`${CONTROL} resize-y font-mono text-[12px] leading-relaxed ${className ?? ''}`}
    />
  )
}

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select {...rest} className={`${CONTROL} cursor-pointer ${className ?? ''}`}>
      {children}
    </select>
  )
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost'
}

const BUTTON_VARIANT = {
  primary: 'bg-primary text-white border-primary hover:brightness-110',
  secondary: 'bg-surface text-fg border-line hover:border-faint',
  ghost: 'bg-transparent text-muted border-transparent hover:bg-raised hover:text-fg',
}

export function Button({ variant = 'secondary', className, children, ...rest }: ButtonProps) {
  return (
    <button
      {...rest}
      className={`inline-flex items-center justify-center gap-1.5 rounded-[5px] border px-2.5 py-1.5 text-[13px] font-medium transition-[filter,border-color,background-color] disabled:cursor-not-allowed disabled:opacity-50 ${BUTTON_VARIANT[variant]} ${className ?? ''}`}
    >
      {children}
    </button>
  )
}

const NOTICE_TONE = {
  error: 'border-danger/35 bg-danger-soft text-danger',
  warn: 'border-warn/35 bg-warn-soft text-warn',
  info: 'border-line bg-raised text-muted',
}

export function Notice({
  tone,
  children,
}: {
  tone: 'error' | 'warn' | 'info'
  children: ReactNode
}) {
  return (
    <p
      role={tone === 'error' ? 'alert' : undefined}
      className={`rounded-[5px] border px-2.5 py-2 text-[12px] ${NOTICE_TONE[tone]}`}
    >
      {children}
    </p>
  )
}

/** ActivityTag renders whether an account may sign in. */
export function ActivityTag({ active }: { active: boolean }) {
  return <Pill tone={active ? 'ok' : 'neutral'}>{active ? 'Active' : 'Disabled'}</Pill>
}

/**
 * Drawer is the console's editing surface: a right-side panel over a scrim,
 * closed with Escape or the backdrop. Forms live in the body, actions in the
 * footer.
 */
export function Drawer({
  title,
  onClose,
  children,
  footer,
  onSubmit,
}: {
  title: string
  onClose: () => void
  children: ReactNode
  footer: ReactNode
  /** When given, the body is wrapped in a form so Enter submits. */
  onSubmit?: (event: React.FormEvent<HTMLFormElement>) => void
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
      <div className="flex flex-1 flex-col gap-3.5 overflow-y-auto p-4">{children}</div>
      <footer className="flex shrink-0 justify-end gap-2 border-t border-line p-3">{footer}</footer>
    </>
  )

  return (
    <div className="fixed inset-0 z-20 flex justify-end">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="absolute inset-0 bg-ink/45"
      />

      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="drawer-in relative flex h-full w-full max-w-[430px] flex-col border-l border-line bg-surface"
      >
        <header className="flex h-12 shrink-0 items-center justify-between border-b border-line px-4">
          <h2 id={titleId} className="text-[14px] font-semibold text-fg">
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="rounded-[5px] p-1 text-muted transition-colors hover:bg-raised hover:text-fg"
          >
            <X aria-hidden="true" className="size-4" />
            <span className="sr-only">Close</span>
          </button>
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
 * CodeBlock is the console's copy surface: mono, on ink, with the copy action
 * where the eye already is. Install commands and tokens are meant to be taken
 * away, so copying is the primary affordance rather than an afterthought.
 */
export function CodeBlock({
  value,
  label,
  secret = false,
}: {
  value: string
  label?: string
  /** Masks the value until revealed. For tokens, which shoulder-surf badly. */
  secret?: boolean
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
    <div className="flex flex-col gap-1">
      {label ? <span className="label">{label}</span> : null}
      <div className="flex items-stretch gap-px overflow-hidden rounded-[5px] border border-ink-line bg-ink">
        <pre className="min-w-0 flex-1 overflow-x-auto px-2.5 py-2 font-mono text-[12px] leading-relaxed whitespace-pre text-ink-fg">
          {revealed ? value : '•'.repeat(Math.min(value.length, 44))}
        </pre>
        <div className="flex shrink-0 items-start gap-px p-1">
          {secret ? (
            <button
              type="button"
              onClick={() => setRevealed((current) => !current)}
              title={revealed ? 'Hide' : 'Reveal'}
              className="rounded-[4px] p-1.5 text-ink-muted transition-colors hover:bg-ink-raised hover:text-white"
            >
              {revealed ? (
                <EyeOff aria-hidden="true" className="size-3.5" />
              ) : (
                <Eye aria-hidden="true" className="size-3.5" />
              )}
              <span className="sr-only">{revealed ? 'Hide value' : 'Reveal value'}</span>
            </button>
          ) : null}
          <button
            type="button"
            onClick={copy}
            title="Copy"
            className="rounded-[4px] p-1.5 text-ink-muted transition-colors hover:bg-ink-raised hover:text-white"
          >
            {copied ? (
              <Check aria-hidden="true" className="size-3.5 text-ok" />
            ) : (
              <Copy aria-hidden="true" className="size-3.5" />
            )}
            <span className="sr-only">{copied ? 'Copied' : 'Copy to clipboard'}</span>
          </button>
        </div>
      </div>
    </div>
  )
}

/** Panel is the standard bordered container with a compact header. */
export function Panel({
  title,
  actions,
  children,
  className,
}: {
  title: string
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`panel ${className ?? ''}`}>
      <header className="flex min-h-10 items-center justify-between gap-3 border-b border-line-soft px-3.5 py-2">
        <h2 className="text-[13px] font-semibold text-fg">{title}</h2>
        {actions}
      </header>
      {children}
    </section>
  )
}
