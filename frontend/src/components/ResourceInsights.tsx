/*
 * The pilot header: what a list is, above the list itself.
 *
 * One skeleton, every resource kind. It used to appear over Pods and the three
 * workload kinds and nowhere else, which meant the page above the table changed
 * height and shape depending on which row of the tree was clicked — so the eye
 * had no fixed place to look and the band was worth less than the space it took.
 * Now every list has one, and what differs per kind is only what is honest to
 * put in it (`lib/insights.ts` decides that; this file only draws it).
 *
 * Three regions, left to right, because they answer three different questions:
 *
 *   1. **how much, and is it all right** — the one or two readings drawn large,
 *      with a state sentence under them and live usage where the cluster serves
 *      it. This region is never empty: every kind has a total.
 *   2. **broken down how** — the state or composition list. Empty buckets are
 *      not drawn at all, so a healthy namespace shows two rows rather than a
 *      column of zeroes, and the one non-zero number is easier to find.
 *   3. **spread over what** — namespaces for most kinds, roles for Nodes, API
 *      groups for CRDs. Drawn only where there is a spread to draw.
 *
 * Under them, one strip: the objects worth naming now, and the fold control.
 *
 * Folding is the concession the band makes to the work. Everything here is
 * derived, so it costs no read, but it does cost vertical space above a table
 * somebody is trying to read — so it folds to a single line and **remembers the
 * choice**, because an operator who folds it does not want it back on the next
 * resource. What the folded line keeps is the total, the state sentence and the
 * mono summary: folding must not cost the reader the reason they would have
 * unfolded it.
 */

import { useState } from 'react'
import { AlertTriangle, ChevronsDownUp, ChevronsUpDown } from 'lucide-react'
import type {
  InsightBucket,
  InsightDistribution,
  InsightStat,
  ResourceInsight,
} from '../lib/insights'
import { TONE_FILL, TONE_TEXT } from '../lib/status'
import { formatCPU, formatMemory } from '../lib/units'

/**
 * The fold is a preference, not page state: it is stored the way the panel's own
 * collapse is, so it survives a navigation between resources and a reload. One
 * key for the whole of Explore rather than one per kind — it is a statement
 * about how much chrome somebody wants over a table, and that does not change
 * between Pods and Services.
 */
const FOLD_KEY = 'kubemg_explore_header_folded'

function readFolded(): boolean {
  try {
    return localStorage.getItem(FOLD_KEY) === '1'
  } catch {
    // Private-mode storage refusals are not worth breaking a header over.
    return false
  }
}

function writeFolded(folded: boolean) {
  try {
    localStorage.setItem(FOLD_KEY, folded ? '1' : '0')
  } catch {
    /* ignored, as above */
  }
}

/**
 * The eight composition slots, as the class names Tailwind will actually emit —
 * literals rather than an interpolation, because Tailwind reads the source for
 * class names and a template string compiles to a rule that does not exist. The
 * order *is* the colour-blindness mechanism (see index.css): never reorder it,
 * and never add a ninth. A slice never rests on its colour alone — the list
 * beside the bar writes every band out by name.
 */
const SLICE_FILL = [
  'bg-chart-1',
  'bg-chart-2',
  'bg-chart-3',
  'bg-chart-4',
  'bg-chart-5',
  'bg-chart-6',
  'bg-chart-7',
  'bg-chart-8',
] as const

/**
 * How many regions the band divides into. Literal classes for the same reason as
 * above; the count is decided by how much this particular kind has to say, so a
 * ConfigMap list does not draw two empty thirds.
 */
const REGION_COLUMNS: Record<number, string> = {
  1: '',
  2: 'lg:grid-cols-2',
  3: 'lg:grid-cols-3',
}

export function ResourceInsights({
  insight,
  bucket,
  onBucket,
  onOpen,
}: {
  insight: ResourceInsight
  /** The bucket the list is currently narrowed to, or null for the whole list. */
  bucket: InsightBucket | null
  onBucket: (next: InsightBucket | null) => void
  /** Opens one alerting object, which is where a header full of names should lead. */
  onOpen?: (name: string, namespace: string) => void
}) {
  const [folded, setFolded] = useState(readFolded)

  function toggleFold() {
    const next = !folded
    setFolded(next)
    writeFolded(next)
  }

  const { lead, breakdown, distribution, alerts, alerting, usage, headline, summary } = insight
  const hidden = alerting - alerts.length
  const total = lead[0]

  if (folded) {
    return (
      <section className="card flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
        <p className="flex shrink-0 items-baseline gap-2">
          <span className="font-mono text-[17px] leading-none font-semibold text-fg tabular-nums">
            {total.value}
          </span>
          <span className="label">{total.label}</span>
        </p>
        <span aria-hidden="true" className="h-4 w-px shrink-0 bg-line" />
        <StateLine tone={insight.headlineTone} headline={headline} />
        {alerting > 0 ? (
          <span className="shrink-0 text-[12px] text-warn">
            {alerting} need{alerting === 1 ? 's' : ''} attention
          </span>
        ) : null}
        {summary.length > 0 ? (
          <span className="ml-auto font-mono text-[11.5px] text-faint tabular-nums">
            {summary.join(' · ')}
          </span>
        ) : null}
        <FoldButton folded onClick={toggleFold} className={summary.length > 0 ? '' : 'ml-auto'} />
      </section>
    )
  }

  // A region is dropped rather than drawn empty: a kind with nothing to break
  // down and nothing to spread over is one column wide, not three with two
  // blanks in them.
  const regions = [
    <div key="lead" className="flex min-w-0 flex-col gap-3 px-4 py-3.5">
      <div className="flex flex-wrap items-start gap-x-7 gap-y-3">
        {lead.map((stat) => (
          <Lead
            key={stat.label}
            stat={stat}
            active={isActive(stat, bucket)}
            onSelect={selector(stat, bucket, onBucket)}
          />
        ))}
      </div>
      <StateLine tone={insight.headlineTone} headline={headline} />
      {usage ? (
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 border-t border-line-soft pt-3">
          <span className="label">In use</span>
          <span className="font-mono text-[13px] text-fg tabular-nums">
            {formatCPU(usage.cpu)} · {formatMemory(usage.memory)}
          </span>
          <span className="text-[11px] text-faint">
            sampled on {usage.sampled} of {total.value}
          </span>
        </div>
      ) : null}
    </div>,
    breakdown.length > 0 ? (
      <div key="breakdown" className="flex min-w-0 flex-col gap-1 px-4 py-3.5">
        {breakdown.map((stat) => (
          <BreakdownRow
            key={stat.label}
            stat={stat}
            active={isActive(stat, bucket)}
            onSelect={selector(stat, bucket, onBucket)}
          />
        ))}
      </div>
    ) : null,
    distribution ? <Distribution key="distribution" distribution={distribution} /> : null,
  ].filter((region) => region !== null)

  return (
    <section className="card overflow-hidden">
      <div
        className={`grid divide-y divide-line-soft lg:divide-x lg:divide-y-0 ${REGION_COLUMNS[regions.length] ?? ''}`}
      >
        {regions}
      </div>

      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 border-t border-line-soft bg-raised/40 px-4 py-2.5">
        {alerts.length > 0 ? (
          <>
            <AlertTriangle
              aria-hidden="true"
              className={`size-3.5 shrink-0 ${alerts[0].tone === 'bad' ? 'text-danger' : 'text-warn'}`}
            />
            {alerts.map((alert) => {
              const title = `${alert.namespace ? `${alert.namespace}/` : ''}${alert.name} — ${alert.reason}`
              const body = (
                <>
                  <span className="font-mono">{alert.name}</span>
                  <span className="opacity-80"> · {alert.reason}</span>
                </>
              )
              const tint =
                alert.tone === 'bad' ? 'bg-danger-soft text-danger' : 'bg-warn-soft text-warn'

              return onOpen ? (
                <button
                  key={alert.key}
                  type="button"
                  onClick={() => onOpen(alert.name, alert.namespace)}
                  title={title}
                  className={`max-w-full rounded-chip px-2 py-0.5 text-[12px] transition-colors hover:opacity-80 ${tint}`}
                >
                  {body}
                </button>
              ) : (
                <span
                  key={alert.key}
                  title={title}
                  className={`rounded-chip px-2 py-0.5 text-[12px] ${tint}`}
                >
                  {body}
                </span>
              )
            })}
            {hidden > 0 ? <span className="text-[12px] text-faint">and {hidden} more</span> : null}
          </>
        ) : null}
        <FoldButton folded={false} onClick={toggleFold} className="ml-auto" />
      </div>
    </section>
  )
}

/** Whether a reading is the one the list is currently narrowed to. */
function isActive(stat: InsightStat, bucket: InsightBucket | null): boolean {
  return bucket !== null && stat.selectable && bucket === stat.id && stat.id !== 'all'
}

/**
 * The click handler for a reading, or undefined where there is nothing behind
 * it. `all` toggles the narrowing off rather than narrowing to everything, which
 * is what a person clicking the total after clicking a bucket means.
 */
function selector(
  stat: InsightStat,
  bucket: InsightBucket | null,
  onBucket: (next: InsightBucket | null) => void,
): (() => void) | undefined {
  if (!stat.selectable) return undefined
  return () => onBucket(stat.id === 'all' || bucket === stat.id ? null : stat.id)
}

/** The state sentence, with a dot so it reads in greyscale as well as in colour. */
function StateLine({ tone, headline }: { tone: InsightStat['tone']; headline: string }) {
  return (
    <p className="flex min-w-0 items-center gap-2">
      <span
        aria-hidden="true"
        className={`size-2 shrink-0 rounded-full ${tone ? TONE_FILL[tone] : 'bg-faint'}`}
      />
      <span className="text-[12.5px] text-muted">{headline}</span>
    </p>
  )
}

/**
 * One of the readings drawn large. It is a button wherever clicking it narrows
 * the list, because a count somebody is looking at is almost always a count they
 * want the rows for; a reading with no rows behind it stays plain text rather
 * than becoming a control that does nothing.
 */
function Lead({
  stat,
  active,
  onSelect,
}: {
  stat: InsightStat
  active: boolean
  onSelect?: () => void
}) {
  const value = (
    <>
      <span
        className={`block font-mono text-[30px] leading-none font-semibold tabular-nums ${
          stat.tone ? TONE_TEXT[stat.tone] : 'text-fg'
        }`}
      >
        {stat.value}
      </span>
      <span className="label mt-2 block whitespace-nowrap">{stat.label}</span>
    </>
  )

  if (!onSelect) return <div className="min-w-0 px-1">{value}</div>

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      className={`min-w-0 rounded-control px-1.5 py-1 text-left transition-colors ${
        active ? 'bg-accent-soft' : 'hover:bg-raised'
      }`}
    >
      {value}
    </button>
  )
}

/**
 * One row of the breakdown: a swatch carrying the tone, the name, and the count
 * on the right in mono so a column of them lines up. The swatch is a square
 * rather than the dot a `Pill` carries, so a reading in a list is never mistaken
 * for a live state indicator.
 */
function BreakdownRow({
  stat,
  active,
  onSelect,
}: {
  stat: InsightStat
  active: boolean
  onSelect?: () => void
}) {
  const body = (
    <>
      <span
        aria-hidden="true"
        className={`size-[7px] shrink-0 rounded-[2px] ${stat.tone ? TONE_FILL[stat.tone] : 'bg-faint'}`}
      />
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-muted">
        {stat.label}
        {stat.detail ? <span className="text-faint"> {stat.detail}</span> : null}
      </span>
      <span className="font-mono text-[12.5px] font-semibold text-fg tabular-nums">
        {stat.value}
      </span>
    </>
  )

  if (!onSelect) {
    return <div className="flex items-center gap-2.5 px-1.5 py-1">{body}</div>
  }

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      className={`flex items-center gap-2.5 rounded-control px-1.5 py-1 text-left transition-colors ${
        active ? 'bg-accent-soft' : 'hover:bg-raised'
      }`}
    >
      {body}
    </button>
  )
}

/**
 * The composition band: one stacked bar over its own legend. The bar is the
 * glance and the list is the answer — the widths carry no numbers, so every band
 * is written out below it with its count and its share.
 */
function Distribution({ distribution }: { distribution: InsightDistribution }) {
  return (
    <div className="flex min-w-0 flex-col gap-2.5 px-4 py-3.5">
      <p className="label">{distribution.label}</p>
      {/* The bar carries no text of its own — the legend under it is the
          readable version, so this is decoration to a screen reader. */}
      <div aria-hidden="true" className="flex h-2 gap-px overflow-hidden rounded-chip">
        {distribution.slices.map((slice) => (
          <span
            key={slice.key}
            className={SLICE_FILL[slice.slot]}
            // Geometry, not colour: a share is a number the deck has no token
            // for, so it is the one thing here set inline.
            style={{ width: `${Math.max(slice.share * 100, 1.5)}%` }}
          />
        ))}
      </div>
      <div className="flex flex-col gap-1">
        {distribution.slices.map((slice) => (
          <div key={slice.key} className="flex items-center gap-2.5">
            <span
              aria-hidden="true"
              className={`size-[7px] shrink-0 rounded-[2px] ${SLICE_FILL[slice.slot]}`}
            />
            <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-muted">
              {slice.label}
            </span>
            <span className="font-mono text-[12px] font-semibold text-fg tabular-nums">
              {slice.value}
            </span>
            <span className="w-9 text-right font-mono text-[11.5px] text-faint tabular-nums">
              {Math.round(slice.share * 100)}%
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

/** The fold control. It carries a word as well as a glyph, like every other state here. */
function FoldButton({
  folded,
  onClick,
  className,
}: {
  folded: boolean
  onClick: () => void
  className?: string
}) {
  const Icon = folded ? ChevronsUpDown : ChevronsDownUp
  return (
    <button
      type="button"
      onClick={onClick}
      aria-expanded={!folded}
      className={`inline-flex shrink-0 items-center gap-1.5 rounded-control px-2 py-1 text-[12px] font-medium text-muted transition-colors hover:bg-raised hover:text-fg ${className ?? ''}`}
    >
      <Icon aria-hidden="true" className="size-3.5" />
      {folded ? 'Details' : 'Fold'}
    </button>
  )
}
