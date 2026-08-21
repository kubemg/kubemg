/*
 * What a list is, in three lines above it.
 *
 * This band used to be a three-column panel: two readings drawn at 30px, a
 * mixed list beside them, a namespace donut, and under it all a wrapping strip
 * of chips. It was about 250px of chrome over a table somebody was trying to
 * read, and the reason it looked arbitrary was not the layout — it was the
 * model. The middle column mixed three different kinds of fact in one list:
 * slices of the total ("Not ready 2"), numbers that cut across it ("Restarts
 * 70"), and numbers that were not slices of anything ("Keys 214", larger than
 * the total). No arrangement of those reads as one thing, because they are not
 * one thing.
 *
 * `lib/insights.ts` now separates them, and this draws the separation:
 *
 *   1. **the line** — how many, and is it all right. The total, the state
 *      sentence, and on the right the scalars that cut across the list
 *      (restarts, replicas, live usage). Always present.
 *   2. **the bar** — the partition, full width, one band per bucket. It is the
 *      only thing here that reads at a glance, and it is deliberately the one
 *      bold mark on the band: a ruler over the table, drawn in the same tones
 *      the table's own state column uses.
 *   3. **the legend** — the same bands as chips, and *these* are the controls.
 *      Clicking one narrows the table. The bar itself is not clickable, because
 *      a 2px sliver is not a hit target; the chip beside it is.
 *
 * Then, only when something is wrong, one line naming the worst two objects and
 * counting the rest. Not the old chip cloud: five padded chips wrapping to two
 * lines read as a tag list, and the names were the least of what they said. The
 * table is better at lists than this band is, so the band names what is worst
 * and hands off.
 *
 * The namespace donut is gone entirely. It was not actionable, and the namespace
 * scope above the list already answers "which namespace" — while the *kinds*
 * that genuinely divide along something worth seeing (a Secret's type, a
 * ClusterRole's author, a CRD's API group) now carry that composition on the bar
 * itself, in the same device as everything else.
 */

import { useState } from 'react'
import type { ReactNode } from 'react'
import { ChevronsDownUp, ChevronsUpDown, Siren, TriangleAlert } from 'lucide-react'
import { Link } from 'react-router'
import type {
  InsightAlert,
  InsightBucket,
  InsightSegment,
  InsightStat,
  ResourceInsight,
} from '../lib/insights'
import { TONE_FILL, TONE_TEXT } from '../lib/status'
import { formatCPU, formatMemory } from '../lib/units'

/**
 * The fold is a preference, not page state: it is stored the way the tree's own
 * collapse is, so it survives a navigation between resources and a reload. One
 * key for the whole console rather than one per kind — it is a statement about
 * how much chrome somebody wants over a table, and that does not change between
 * Pods and Services.
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
 * and never add a ninth. A band never rests on its colour alone — the legend
 * under the bar writes every one of them out by name.
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

/** How many alerting objects the band names before it starts counting instead. */
const NAMED_ALERTS = 2

/** A segment's fill, whichever of the two colour systems it belongs to. */
function segmentFill(segment: InsightSegment): string {
  if (segment.tone) return TONE_FILL[segment.tone]
  return SLICE_FILL[segment.slot ?? 0]
}

export function ResourceInsights({
  insight,
  bucket,
  onBucket,
  onOpen,
  alertHref,
  trend,
}: {
  insight: ResourceInsight
  /** The bucket the list is currently narrowed to, or null for the whole list. */
  bucket: InsightBucket | null
  onBucket: (next: InsightBucket | null) => void
  /** Opens one alerting object, which is where a band naming names should lead. */
  onOpen?: (name: string, namespace: string) => void
  /**
   * Where an alert's *events* live — the cluster's own account of what has been
   * happening to that object, filtered to it.
   *
   * It is a href builder rather than a callback because it produces a link, and
   * a link is the thing that can be middle-clicked, copied and pasted into a
   * ticket. The page supplies it because only the page knows which cluster is
   * open; the band only knows which objects it named.
   */
  alertHref?: (alert: InsightAlert) => string
  /**
   * The trend region, where the open list and the open namespace earn one. It
   * arrives as a node rather than as a cluster and a metric because deciding
   * *whether* there is history worth charting is the page's business — it knows
   * the cluster, the namespace and whether the list is one whose objects consume
   * anything — and drawing the band around it is this component's.
   *
   * It is a full-width row under the legend rather than a third column. A curve
   * needs width to be a curve, and the two things above it are a sentence and a
   * bar, which do not.
   */
  trend?: ReactNode
}) {
  const [folded, setFolded] = useState(readFolded)

  function toggleFold() {
    const next = !folded
    setFolded(next)
    writeFolded(next)
  }

  const { total, segments, readings, alerts, alerting, usage, headline, summary } = insight
  const named = alerts.slice(0, NAMED_ALERTS)
  const rest = alerting - named.length

  if (folded) {
    return (
      <section className="card flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5">
        <Total stat={total} bucket={bucket} onBucket={onBucket} />
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

  return (
    <section className="card overflow-hidden">
      {/* 1 — the line. */}
      <div
        className={`flex flex-wrap items-center gap-x-4 gap-y-2 px-4 pt-3 ${
          segments.length > 0 ? '' : 'pb-3'
        }`}
      >
        <Total stat={total} bucket={bucket} onBucket={onBucket} />
        <StateLine tone={insight.headlineTone} headline={headline} />

        <div className="ml-auto flex flex-wrap items-center gap-x-3 gap-y-1">
          {readings.map((stat) => (
            <Reading
              key={stat.label}
              stat={stat}
              active={isActive(stat, bucket)}
              onSelect={selector(stat, bucket, onBucket)}
            />
          ))}
          {usage ? (
            <span
              title={`Sampled on ${usage.sampled} of ${total.value}`}
              className="font-mono text-[11.5px] text-faint tabular-nums"
            >
              CPU {formatCPU(usage.cpu)} · MEM {formatMemory(usage.memory)}
            </span>
          ) : null}
          <FoldButton folded={false} onClick={toggleFold} />
        </div>
      </div>

      {segments.length > 0 ? (
        <>
          {/* 2 — the bar. Decoration to a screen reader: the legend below is the
              readable version of exactly the same numbers. */}
          <div aria-hidden="true" className="mx-4 mt-3 flex h-1.5 gap-0.5 overflow-hidden rounded-chip">
            {segments.map((segment) => (
              <span
                key={segment.label}
                className={segmentFill(segment)}
                // Geometry, not colour: a share is a number the deck has no
                // token for, so it is the one thing here set inline. The floor
                // keeps a single failing pod out of a thousand visible.
                style={{ width: `${Math.max(segment.share * 100, 1.5)}%` }}
              />
            ))}
          </div>

          {/* 3 — the legend, which is where the clicking happens. */}
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 pt-2.5 pb-3">
            {segments.map((segment) => (
              <SegmentChip
                key={segment.label}
                segment={segment}
                active={isActive(segment, bucket)}
                onSelect={selector(segment, bucket, onBucket)}
              />
            ))}
          </div>
        </>
      ) : null}

      {named.length > 0 ? (
        <div className="flex items-center gap-2 border-t border-line-soft bg-raised/40 px-4 py-2">
          <TriangleAlert
            aria-hidden="true"
            className={`size-3.5 shrink-0 ${
              named[0].tone === 'bad' ? 'text-danger' : 'text-warn'
            }`}
          />
          <div className="flex min-w-0 flex-1 items-center gap-x-2 overflow-hidden">
            {named.map((alert, index) => (
              <span key={alert.key} className="flex min-w-0 items-baseline gap-2">
                {index > 0 ? (
                  <span aria-hidden="true" className="text-faint">
                    ·
                  </span>
                ) : null}
                <Alert alert={alert} onOpen={onOpen} href={alertHref?.(alert)} />
              </span>
            ))}
          </div>
          {rest > 0 ? (
            <span className="shrink-0 text-[12px] text-faint">
              and {rest} more in the table
            </span>
          ) : null}
        </div>
      ) : null}

      {trend ? <div className="border-t border-line-soft">{trend}</div> : null}
    </section>
  )
}

/** Whether a reading is the one the list is currently narrowed to. */
function isActive(stat: { id: InsightBucket; selectable: boolean }, bucket: InsightBucket | null) {
  return bucket !== null && stat.selectable && bucket === stat.id && stat.id !== 'all'
}

/**
 * The click handler for a reading, or undefined where there is nothing behind
 * it. `all` toggles the narrowing off rather than narrowing to everything, which
 * is what a person clicking the total after clicking a bucket means.
 */
function selector(
  stat: { id: InsightBucket; selectable: boolean },
  bucket: InsightBucket | null,
  onBucket: (next: InsightBucket | null) => void,
): (() => void) | undefined {
  if (!stat.selectable) return undefined
  return () => onBucket(stat.id === 'all' || bucket === stat.id ? null : stat.id)
}

/**
 * How many rows there are, and the way back to all of them. It is a button only
 * while a narrowing is active — that is the only time clicking the total does
 * anything, and a control that does nothing is worse than a number.
 */
function Total({
  stat,
  bucket,
  onBucket,
}: {
  stat: InsightStat
  bucket: InsightBucket | null
  onBucket: (next: InsightBucket | null) => void
}) {
  const body = (
    <>
      <span className="font-mono text-[19px] leading-none font-semibold text-fg tabular-nums">
        {stat.value}
      </span>
      <span className="label">{stat.label}</span>
    </>
  )

  if (!stat.selectable || bucket === null) {
    return <p className="flex shrink-0 items-baseline gap-2">{body}</p>
  }

  return (
    <button
      type="button"
      onClick={() => onBucket(null)}
      title="Show every row again"
      className="-mx-1.5 flex shrink-0 items-baseline gap-2 rounded-control px-1.5 py-0.5 transition-colors hover:bg-raised"
    >
      {body}
    </button>
  )
}

/** The state sentence, with a dot so it reads in greyscale as well as in colour. */
function StateLine({ tone, headline }: { tone: InsightStat['tone']; headline: string }) {
  return (
    <p className="flex min-w-0 items-center gap-2">
      <span
        aria-hidden="true"
        className={`size-2 shrink-0 rounded-full ${tone ? TONE_FILL[tone] : 'bg-faint'}`}
      />
      <span className="truncate text-[12.5px] text-muted">{headline}</span>
    </p>
  )
}

/**
 * One band of the bar, written out. The count leads because that is what is
 * being compared down the row; the label follows it as prose, so `9 Running`
 * reads as a phrase rather than as a cell in a table that is not there.
 */
function SegmentChip({
  segment,
  active,
  onSelect,
}: {
  segment: InsightSegment
  active: boolean
  onSelect?: () => void
}) {
  const body = (
    <>
      <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${segmentFill(segment)}`} />
      <span className="font-mono text-[13px] font-semibold text-fg tabular-nums">
        {segment.value}
      </span>
      <span className="min-w-0 truncate text-[12.5px] text-muted">
        {segment.label}
        {segment.detail ? <span className="text-faint"> {segment.detail}</span> : null}
      </span>
    </>
  )

  if (!onSelect) {
    return <span className="flex min-w-0 items-center gap-1.5">{body}</span>
  }

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      title={active ? `Showing only ${segment.label}` : `Show only ${segment.label}`}
      className={`-mx-1.5 flex min-w-0 items-center gap-1.5 rounded-control px-1.5 py-0.5 transition-colors ${
        active ? 'bg-accent-soft ring-1 ring-accent-line ring-inset' : 'hover:bg-raised'
      }`}
    >
      {body}
    </button>
  )
}

/**
 * A scalar that is true of the list without being a slice of it. Set small and
 * on the right, because it is the kind of thing you read once and then stop
 * looking at — unlike the bar, which you glance at every time the list reloads.
 */
function Reading({
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
        className={`font-mono text-[12.5px] font-semibold tabular-nums ${
          stat.tone ? TONE_TEXT[stat.tone] : 'text-fg'
        }`}
      >
        {stat.value}
      </span>
      <span className="text-[12px] text-muted">{stat.label}</span>
      {stat.detail ? <span className="text-[11.5px] text-faint">{stat.detail}</span> : null}
    </>
  )

  if (!onSelect) {
    return <span className="flex shrink-0 items-baseline gap-1.5">{body}</span>
  }

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      className={`-mx-1.5 flex shrink-0 items-baseline gap-1.5 rounded-control px-1.5 py-0.5 transition-colors ${
        active ? 'bg-accent-soft ring-1 ring-accent-line ring-inset' : 'hover:bg-raised'
      }`}
    >
      {body}
    </button>
  )
}

/**
 * One named object worth looking at now.
 *
 * An alert raises a question with two honest next steps, and they are different
 * questions: "show me this object" (the drawer) and "what has the cluster been
 * saying about it" (its events). The name opens the object and the trailing
 * glyph opens the timeline, filtered to it.
 *
 * No chip fill. Five tinted chips wrapping to two lines was the thing that made
 * this strip read as a tag cloud; the tone on the reason is enough to say which
 * of two named objects is the worse one.
 */
function Alert({
  alert,
  onOpen,
  href,
}: {
  alert: InsightAlert
  onOpen?: (name: string, namespace: string) => void
  href?: string
}) {
  const title = `${alert.namespace ? `${alert.namespace}/` : ''}${alert.name} — ${alert.reason}`
  const tint = alert.tone === 'bad' ? 'text-danger' : 'text-warn'

  const body = (
    <>
      <span className="min-w-0 truncate font-mono text-[12px] text-fg">{alert.name}</span>
      <span className={`shrink-0 text-[12px] ${tint}`}>{alert.reason}</span>
    </>
  )

  return (
    <span className="flex min-w-0 items-baseline gap-1.5">
      {onOpen ? (
        <button
          type="button"
          onClick={() => onOpen(alert.name, alert.namespace)}
          title={title}
          className="flex min-w-0 items-baseline gap-1.5 rounded-control transition-opacity hover:opacity-70"
        >
          {body}
        </button>
      ) : (
        <span title={title} className="flex min-w-0 items-baseline gap-1.5">
          {body}
        </span>
      )}
      {href ? (
        <Link
          to={href}
          title={`What the cluster recorded about ${alert.name}`}
          className={`shrink-0 transition-opacity hover:opacity-70 ${tint}`}
        >
          <Siren aria-hidden="true" className="size-3.5" />
          <span className="sr-only">Events for {alert.name}</span>
        </Link>
      ) : null}
    </span>
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
