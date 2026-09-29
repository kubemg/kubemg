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
import type { StripFigure } from '../lib/fleetStrip'

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
      className="flex flex-wrap items-stretch overflow-hidden rounded-card border border-line bg-surface"
    >
      {figures.map((figure) => (
        <StripLink key={figure.key} figure={figure} />
      ))}
      {children}
    </section>
  )
}

function StripLink({ figure }: { figure: StripFigure }) {
  const { value } = figure
  const reading = typeof value === 'number'
  const lit = reading && value > 0

  // Unknown and not-yet-read are both *not a number*, and both must look it: a
  // dash for a read that failed, an ellipsis for one still in flight. Neither is
  // ever drawn as 0.
  const shown = reading ? String(value) : value === null ? '—' : '…'
  const tone = !reading
    ? 'text-muted'
    : lit
      ? figure.tone === 'bad'
        ? 'font-bold text-danger'
        : 'font-bold text-warn'
      : 'font-bold text-fg'

  return (
    <Link
      to={figure.to}
      title={value === null ? 'Could not be read — open the page for the rows' : undefined}
      className="group min-w-[128px] border-r border-line-soft px-5 py-3 last:border-r-0 hover:bg-raised"
    >
      <p className={`font-mono text-[21px] leading-tight tabular-nums ${tone}`}>
        {shown}
        {value === null ? <span className="sr-only"> (could not be read)</span> : null}
        {value === undefined ? <span className="sr-only"> (reading)</span> : null}
      </p>
      <p className="label mt-0.5 group-hover:text-accent">{figure.label}</p>
    </Link>
  )
}
