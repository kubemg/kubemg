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
import { FileKey, Gauge, Server, ShieldX, Timer } from 'lucide-react'
import type { StripFigure } from '../lib/fleetStrip'
import { StatTile } from './primitives'

/** Each figure's glyph, by key. A key with none here gets the gauge. */
const FIGURE_ICON: Record<string, typeof Gauge> = {
  requests: Timer,
  refused: ShieldX,
  expiring: FileKey,
  behind: Server,
}

export function FleetStrip({
  figures,
  className,
  children,
}: {
  figures: StripFigure[]
  /** The grid, where a page lays the tiles out other than four across. */
  className?: string
  children?: ReactNode
}) {
  return (
    <section
      aria-label="Needs a decision"
      className={className ?? 'grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4'}
    >
      {figures.map((figure) => (
        <StripLink key={figure.key} figure={figure} />
      ))}
      {children}
    </section>
  )
}

/* One figure, as a tile, tinted only when it is asking for something. */
function StripLink({ figure }: { figure: StripFigure }) {
  const { value } = figure
  const reading = typeof value === 'number'
  const lit = reading && value > 0

  // Unknown and not-yet-read are both *not a number*, and both must look it: a
  // dash for a read that failed, an ellipsis for one still in flight. Neither is
  // ever drawn as 0.
  const shown = reading ? String(value) : value === null ? '—' : '…'

  return (
    <StatTile
      to={figure.to}
      title={value === null ? 'Could not be read — open the page for the rows' : undefined}
      icon={FIGURE_ICON[figure.key] ?? Gauge}
      label={figure.label}
      tone={lit ? (figure.tone === 'bad' ? 'danger' : 'warn') : 'neutral'}
      dim={!reading}
      value={
        <>
          {shown}
          {value === null ? <span className="sr-only"> (could not be read)</span> : null}
          {value === undefined ? <span className="sr-only"> (reading)</span> : null}
        </>
      }
    />
  )
}
