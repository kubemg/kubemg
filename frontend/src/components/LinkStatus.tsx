import { Cable, CircleDashed, Unplug, Waypoints } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// One definition, in the module that derives it: a component that redeclared
// the four states could drift from the function that decides between them.
export type { LinkState } from '../lib/status'
import type { LinkState } from '../lib/status'
import { LINK_LABEL } from '../lib/status'

/*
 * The link, said rather than drawn. This used to be a strand — a coloured
 * trace running the width of a card or a rail row — and a green or red band is
 * the loudest thing on a surface whose job is to be read, not watched. What an
 * operator needs from it is one fact ("can KubeMG reach this cluster right
 * now") and that fact is a glyph and a word, which costs no horizontal run,
 * survives greyscale on the shape of the glyph, and does not compete with the
 * cluster's own name for the eye.
 */
const ICON: Record<LinkState, LucideIcon> = {
  /* two nodes met in the middle: the cluster dialled out and KubeMG answered */
  live: Waypoints,
  /* a fixed run of cable: KubeMG dials the API server itself */
  direct: Cable,
  /* a plug pulled apart, cut clean — the one silhouette that reads at 14px */
  down: Unplug,
  /* not drawn solid yet: nothing has dialled in */
  idle: CircleDashed,
}

/* The link reads the way the deck's status badges do: a soft plate in the
   state's tone with a hairline edge in the same tone, the glyph inside it.
   `ok` and `danger` are state and stay semantic wherever they land. The two
   neutral states are not state, they are the absence of it, so on the rail they
   take the rail's own quiet tokens rather than the work palette's — a `muted`
   borrowed from the page is the wrong grey against chrome, on either deck. */
const TONE: Record<LinkState, string> = {
  live: 'border-ok/40 bg-ok-soft text-ok',
  direct: 'border-line bg-raised text-muted',
  down: 'border-danger/40 bg-danger-soft text-danger',
  idle: 'border-dashed border-faint/60 bg-raised text-faint',
}

const RAIL_TONE: Record<LinkState, string> = {
  live: 'border-ok/40 bg-ok-soft text-ok',
  direct: 'border-rail-line bg-rail text-rail-muted',
  down: 'border-danger/40 bg-danger-soft text-danger',
  idle: 'border-dashed border-rail-line bg-rail text-rail-faint',
}

const READING: Record<LinkState, string> = {
  live: 'Agent tunnel open — traffic flows cluster to kubemg',
  direct: 'kubemg dials this cluster directly',
  down: 'No link to this cluster',
  idle: 'Waiting for the cluster to dial in',
}

/**
 * LinkStatus is how a cluster's link to KubeMG reads everywhere it is shown.
 * `detail` is the glyph with its word in a badge, for a card, a table cell or
 * a path; `glyph` is the glyph alone in a round plate, for the rail, the palette and the cluster menu,
 * where the row is already carrying a name and a version and a third piece of
 * text would make it a paragraph. A live link breathes, slowly — it is the one
 * thing on the deck that is genuinely still happening.
 */
export function LinkStatus({
  state,
  variant = 'detail',
  surface = 'work',
  label,
  className,
}: {
  state: LinkState
  variant?: 'detail' | 'glyph'
  surface?: 'work' | 'rail'
  label?: string
  className?: string
}) {
  const Icon = ICON[state]
  const reading = READING[state]
  const tone = surface === 'rail' ? RAIL_TONE[state] : TONE[state]

  if (variant === 'glyph') {
    return (
      <span
        role="img"
        aria-label={reading}
        title={reading}
        className={`grid size-5 shrink-0 place-items-center rounded-full border ${tone} ${className ?? ''}`}
      >
        <Icon aria-hidden="true" className="size-3" />
      </span>
    )
  }

  return (
    <span
      title={reading}
      className={`inline-flex max-w-full min-w-0 items-center gap-1.5 rounded-full border px-2 py-px text-[12px] font-medium whitespace-nowrap ${tone} ${className ?? ''}`}
    >
      <Icon
        aria-hidden="true"
        className={`size-3 shrink-0 ${state === 'live' ? 'link-live' : ''}`}
      />
      <span className="min-w-0 truncate">{label ?? LINK_LABEL[state]}</span>
    </span>
  )
}

/**
 * PathNode is an endpoint on the path traffic takes — a cluster, KubeMG itself,
 * or the caller. Used where the whole route is worth drawing rather than just
 * whether the link is up.
 */
export function PathNode({
  label,
  value,
  tone = 'idle',
}: {
  label: string
  value: string
  tone?: 'ok' | 'idle' | 'accent'
}) {
  const dot = tone === 'ok' ? 'bg-ok' : tone === 'accent' ? 'bg-accent' : 'bg-faint'

  return (
    <span className="flex min-w-0 flex-col gap-1">
      <span className="flex items-center gap-1.5">
        <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${dot}`} />
        <span className="label">{label}</span>
      </span>
      <span className="truncate font-data text-[13px] text-fg" title={value}>
        {value}
      </span>
    </span>
  )
}

/**
 * PathHop is one leg of that route: what the link is doing, and what that leg
 * of the journey actually is. The direction is a chevron rather than a drawn
 * line, because the route is read left to right anyway and a line between two
 * labelled nodes only repeats what their order already says.
 */
export function PathHop({
  state,
  caption,
  label,
}: {
  state: LinkState
  caption: string
  label?: string
}) {
  return (
    <span className="flex min-w-0 flex-1 items-center gap-2 pb-1">
      <span aria-hidden="true" className="hidden shrink-0 text-faint sm:block">
        ›
      </span>
      <span className="min-w-0 flex-1">
        <LinkStatus state={state} label={label} />
        <span className="mt-1 block truncate font-data text-[11px] text-faint" title={caption}>
          {caption}
        </span>
      </span>
      <span aria-hidden="true" className="hidden shrink-0 text-faint sm:block">
        ›
      </span>
    </span>
  )
}
