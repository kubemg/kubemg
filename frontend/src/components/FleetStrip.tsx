/*
 * The fleet's first row, drawn: a figure per decision, each one a link onto the
 * thing it counts. What the figures are, and why these, is `lib/fleetStrip.ts`;
 * this file only decides how a reading, a failed read and a read in flight look,
 * and keeps the three apart.
 *
 * Anything passed as children sits at the strip's right end — the operator's
 * capacity tracks, which are a reading of the fleet rather than a decision.
 */

import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { FileKey, Gauge, Server, ShieldX, Timer } from 'lucide-react'
import type { StripFigure } from '../lib/fleetStrip'

/** Each figure's glyph, by key. A key with none here gets the gauge. */
const FIGURE_ICON: Record<string, typeof Gauge> = {
  requests: Timer,
  refused: ShieldX,
  expiring: FileKey,
  behind: Server,
}

export function FleetStrip({
  figures,
  children,
}: {
  figures: StripFigure[]
  children?: ReactNode
}) {
  return (
    <section
      aria-label="Needs a decision"
      className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4"
    >
      {figures.map((figure) => (
        <StripLink key={figure.key} figure={figure} />
      ))}
      {children}
    </section>
  )
}

/*
 * One figure, as a tile. A figure that is asking for something takes its
 * tone's soft fill, fading into the surface; a zero, a failed read and a read
 * in flight stay neutral, so colour only ever means "this one".
 */
function StripLink({ figure }: { figure: StripFigure }) {
  const { value } = figure
  const reading = typeof value === 'number'
  const lit = reading && value > 0
  const bad = lit && figure.tone === 'bad'
  const Icon = FIGURE_ICON[figure.key] ?? Gauge

  // Unknown and not-yet-read are both *not a number*, and both must look it: a
  // dash for a read that failed, an ellipsis for one still in flight. Neither is
  // ever drawn as 0.
  const shown = reading ? String(value) : value === null ? '—' : '…'
  const figureTone = !reading ? 'text-muted' : bad ? 'text-danger' : lit ? 'text-warn' : 'text-fg'
  const tint = bad ? 'from-danger-soft' : lit ? 'from-warn-soft' : 'from-raised'
  const glyph = bad ? 'text-danger' : lit ? 'text-warn' : 'text-muted'

  return (
    <Link
      to={figure.to}
      title={value === null ? 'Could not be read — open the page for the rows' : undefined}
      className={`group flex flex-col rounded-card border border-line bg-linear-to-b ${tint} to-surface p-4 shadow-deck transition-colors duration-300 hover:border-faint/60`}
    >
      <span
        className={`mb-4 grid size-10 place-items-center rounded-full border border-line-soft bg-surface shadow-deck ${glyph}`}
      >
        <Icon aria-hidden="true" className="size-4.5" />
      </span>
      <span className="text-[14px] text-fg transition-colors duration-300 group-hover:text-accent">
        {figure.label}
      </span>
      <span className={`mt-1 font-mono text-[24px] leading-tight font-bold tabular-nums ${figureTone}`}>
        {shown}
        {value === null ? <span className="sr-only"> (could not be read)</span> : null}
        {value === undefined ? <span className="sr-only"> (reading)</span> : null}
      </span>
    </Link>
  )
}
