import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

/**
 * IntegrationTile is one thing a cluster is wired to — a series backend, a
 * console, a curation of its sidebar — drawn as a card in a grid of its kind.
 *
 * A wired tile is a plate: its glyph in a round chip, the name, its state, what
 * it points at, and its actions along the foot. An unwired one is the same card
 * with a dashed edge and the line that says what it would be for, so a row of
 * them reads as "three of these, two connected" at a glance rather than as a
 * list somebody has to read down.
 */
export function IntegrationTile({
  icon: Icon,
  title,
  meta,
  state,
  wired,
  children,
  link,
  actions,
}: {
  icon: LucideIcon
  title: string
  /** Beside the name, quieter: a provider, a project. */
  meta?: ReactNode
  /** The state pill, top right. */
  state?: ReactNode
  /** Whether there is anything here yet — an unwired tile is dashed. */
  wired: boolean
  /** The body: what it points at, or what it would be for. */
  children?: ReactNode
  /** Bottom left: the way out to the thing itself. */
  link?: ReactNode
  /** Bottom right: what can be done to it here. */
  actions?: ReactNode
}) {
  return (
    <article
      className={`flex min-w-0 flex-col rounded-card border p-4 ${
        wired
          ? 'border-line bg-linear-to-b from-raised/60 to-surface shadow-deck'
          : 'border-dashed border-line bg-surface'
      }`}
    >
      <header className="flex items-start gap-3">
        <span
          aria-hidden="true"
          className={`grid size-9 shrink-0 place-items-center rounded-full border border-line-soft bg-surface ${
            wired ? 'text-fg shadow-deck' : 'text-faint'
          }`}
        >
          <Icon className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2">
            <h4 className="truncate text-[14px] font-semibold text-fg">{title}</h4>
            {state ? <span className="ml-auto flex shrink-0">{state}</span> : null}
          </div>
          {meta ? <div className="mt-0.5 truncate text-[12.5px] text-muted">{meta}</div> : null}
        </div>
      </header>

      {children ? <div className="mt-3 min-w-0 text-[12px] leading-relaxed">{children}</div> : null}

      {link || actions ? (
        <footer className="mt-auto flex min-h-8 items-center gap-2 pt-3">
          <span className="min-w-0 flex-1">{link}</span>
          {actions ? <span className="flex shrink-0 items-center gap-1">{actions}</span> : null}
        </footer>
      ) : null}
    </article>
  )
}

/** The grid a set of tiles sits in: one column on a phone, three on a desk. */
export const INTEGRATION_GRID = 'grid gap-3 sm:grid-cols-2 xl:grid-cols-3'

/**
 * IntegrationGroup is one set of tiles under its own heading, for a panel that
 * holds several sets — the heading carries the set's actions so they sit with
 * the tiles they act on rather than at the top of the whole panel.
 */
export function IntegrationGroup({
  title,
  description,
  actions,
  children,
}: {
  title: string
  description?: string
  actions?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="flex flex-col gap-3">
      <header className="flex flex-wrap items-end justify-between gap-x-3 gap-y-1">
        <div className="min-w-0">
          <h3 className="text-[14px] font-semibold text-fg">{title}</h3>
          {description ? (
            <p className="mt-0.5 max-w-2xl text-[12.5px] leading-relaxed text-muted">{description}</p>
          ) : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
      </header>
      {children}
    </section>
  )
}
