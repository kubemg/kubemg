import { useId, useMemo, useState } from 'react'
import type { CostSummary, CostedNamespace } from '../api/types'
import { formatMoney } from '../lib/units'

/*
 * The two charts the cost page is built on, drawn as SVG against the deck's own
 * tokens — same rule as the time-series chart: there is no charting library
 * here, series colour comes from the eight validated chart slots in order and
 * is never cycled, and identity never rests on colour alone.
 *
 * Both of these draw *money*, which changes one thing about how they are built.
 * A cost figure is an estimate over rates somebody typed in, so neither chart
 * is allowed to be the only place a number appears: every value drawn here is
 * also written out in the list beneath it. That is the accessibility floor and
 * it is also the honesty floor — a reader who cannot hover must not have to
 * take the picture's word for it.
 */

/** The eight chart slots, as literals Tailwind can actually see. */
const SLOT_FILL = [
  'fill-chart-1',
  'fill-chart-2',
  'fill-chart-3',
  'fill-chart-4',
  'fill-chart-5',
  'fill-chart-6',
  'fill-chart-7',
  'fill-chart-8',
] as const

const SLOT_SWATCH = [
  'bg-chart-1',
  'bg-chart-2',
  'bg-chart-3',
  'bg-chart-4',
  'bg-chart-5',
  'bg-chart-6',
  'bg-chart-7',
  'bg-chart-8',
] as const

const MAX_SLOTS = SLOT_FILL.length

/* ------------------------------------------------------------ the split --- */

/**
 * What the fleet costs, and how much of it anything has claimed.
 *
 * One track, because the two figures share a denominator: the whole bar is
 * every node's allocatable capacity at the rate card, the filled part is what
 * the workloads reserved, and the remainder is capacity bought and not claimed.
 * Drawing them as two bars would invite adding them up, and they are already a
 * total and a part of it.
 *
 * The unclaimed remainder is deliberately not coloured as a fault. A cluster
 * needs headroom to schedule into and to survive a node failing; whether 40%
 * is prudent or wasteful is a judgement about *this* fleet that no threshold
 * here could make.
 */
export function AllocationSplit({
  summary,
  currency,
}: {
  summary: CostSummary
  currency: string
}) {
  const total = summary.infrastructure_monthly.total
  const claimed = Math.min(100, Math.max(0, summary.attributed_percent))
  const measured = total > 0

  return (
    <div className="min-w-0">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="label">Nodes, per month</span>
        <span className="ml-auto font-mono text-[18px] font-semibold text-fg tabular-nums">
          {formatMoney(total, currency)}
        </span>
      </div>

      <div
        role="meter"
        aria-label="Share of the fleet's cost that workloads have reserved"
        aria-valuenow={measured ? Math.round(summary.attributed_percent) : undefined}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={
          measured
            ? `${formatMoney(summary.attributed_monthly.total, currency)} reserved by workloads ` +
              `of ${formatMoney(total, currency)} total, ` +
              `${formatMoney(summary.unallocated_monthly.total, currency)} unclaimed`
            : 'no rates or no nodes'
        }
        className="relative mt-2 h-3 overflow-hidden rounded-full bg-raised"
      >
        <span
          aria-hidden="true"
          className="block h-full rounded-full bg-chart-1"
          style={{ width: `${claimed}%` }}
        />
      </div>

      <dl className="mt-3 grid gap-x-4 gap-y-2 sm:grid-cols-2">
        <SplitLine
          slot={0}
          term="Reserved by workloads"
          value={formatMoney(summary.attributed_monthly.total, currency)}
          hint={`${summary.attributed_percent.toFixed(0)}% of the fleet`}
        />
        <SplitLine
          term="Bought and unclaimed"
          value={formatMoney(summary.unallocated_monthly.total, currency)}
          hint="headroom, or capacity to give back"
        />
      </dl>
    </div>
  )
}

function SplitLine({
  slot,
  term,
  value,
  hint,
}: {
  slot?: number
  term: string
  value: string
  hint: string
}) {
  return (
    <div className="min-w-0">
      <dt className="flex items-center gap-1.5">
        <span
          aria-hidden="true"
          className={`size-2 shrink-0 rounded-full ${
            slot === undefined ? 'bg-raised' : SLOT_SWATCH[slot]
          }`}
        />
        <span className="text-[12.5px] text-muted">{term}</span>
      </dt>
      <dd className="ml-3.5 font-mono text-[13px] text-fg tabular-nums">
        {value} <span className="text-[11.5px] text-faint">· {hint}</span>
      </dd>
    </div>
  )
}

/* ---------------------------------------------------------- the treemap --- */

interface Tile {
  namespace: string
  value: number
  slot: number
  x: number
  y: number
  width: number
  height: number
}

/**
 * Namespace cost as a treemap.
 *
 * A treemap is the right shape here for one reason: cost is a *part of a whole*
 * and the parts are wildly unequal. On a real fleet two namespaces are most of
 * the bill and forty are a rounding error, which a bar chart renders as two
 * bars and forty invisible stubs sorted by a length nobody can compare. Area
 * survives that ratio — and area is also what makes "this one namespace is half
 * the cluster" land without reading a single number.
 *
 * It is squarified rather than sliced: a slice-and-dice layout at these ratios
 * produces slivers a few pixels wide, which cannot be labelled, clicked or
 * compared. Squarifying trades exact ordering for tiles close to square, which
 * is the trade that makes the picture readable.
 *
 * Everything past the eighth namespace folds into one tile rather than
 * inventing a ninth hue, exactly as the time-series chart folds its ninth
 * series — and, like it, every value is written out in the list underneath.
 */
export function NamespaceTreemap({
  namespaces,
  currency,
}: {
  namespaces: CostedNamespace[]
  currency: string
}) {
  const titleId = useId()
  const [hovered, setHovered] = useState<string | null>(null)

  const width = 100
  const height = 58

  const { tiles, folded } = useMemo(() => {
    const ranked = namespaces.filter((entry) => entry.monthly.total > 0)
    if (ranked.length === 0) return { tiles: [] as Tile[], folded: 0 }

    const head = ranked.slice(0, MAX_SLOTS - 1)
    const tail = ranked.slice(MAX_SLOTS - 1)
    const entries = head.map((entry, index) => ({
      namespace: entry.namespace,
      value: entry.monthly.total,
      slot: index,
    }))
    if (tail.length > 0) {
      entries.push({
        namespace: `${tail.length} more`,
        value: tail.reduce((sum, entry) => sum + entry.monthly.total, 0),
        slot: MAX_SLOTS - 1,
      })
    }
    return { tiles: squarify(entries, width, height), folded: tail.length }
  }, [namespaces])

  if (tiles.length === 0) return null

  const total = tiles.reduce((sum, tile) => sum + tile.value, 0)

  return (
    <div className="min-w-0">
      <svg
        role="img"
        aria-labelledby={titleId}
        viewBox={`0 0 ${width} ${height}`}
        className="block w-full rounded-control"
        preserveAspectRatio="none"
      >
        <title id={titleId}>
          Estimated monthly cost by namespace. {tiles.map((tile) =>
            `${tile.namespace}, ${formatMoney(tile.value, currency)}. `).join('')}
        </title>
        {tiles.map((tile) => (
          <g key={tile.namespace}>
            <rect
              x={tile.x}
              y={tile.y}
              width={tile.width}
              height={tile.height}
              className={`${SLOT_FILL[tile.slot]} stroke-surface transition-opacity ${
                hovered && hovered !== tile.namespace ? 'opacity-40' : ''
              }`}
              strokeWidth={0.6}
              onPointerEnter={() => setHovered(tile.namespace)}
              onPointerLeave={() => setHovered(null)}
            />
          </g>
        ))}
      </svg>

      {/* The legend is the chart. Every tile's value is here in text, ranked,
          because a namespace whose tile is four pixels tall is still a line an
          operator has to be able to read. */}
      <ul className="mt-3 flex flex-col gap-1">
        {tiles.map((tile) => (
          <li
            key={tile.namespace}
            className={`flex items-baseline gap-2 rounded-control px-1 py-0.5 transition-colors ${
              hovered === tile.namespace ? 'bg-sunken' : ''
            }`}
            onPointerEnter={() => setHovered(tile.namespace)}
            onPointerLeave={() => setHovered(null)}
          >
            <span
              aria-hidden="true"
              className={`size-2 shrink-0 rounded-full ${SLOT_SWATCH[tile.slot]}`}
            />
            <span className="min-w-0 truncate font-mono text-[12px] text-fg">
              {tile.namespace}
            </span>
            <span className="ml-auto shrink-0 font-mono text-[12px] text-muted tabular-nums">
              {formatMoney(tile.value, currency)}
            </span>
            <span className="w-10 shrink-0 text-right font-mono text-[11px] text-faint tabular-nums">
              {total > 0 ? `${Math.round((tile.value / total) * 100)}%` : ''}
            </span>
          </li>
        ))}
      </ul>

      {folded > 0 ? (
        <p className="mt-2 text-[11.5px] text-faint">
          The last tile is {folded} smaller {folded === 1 ? 'namespace' : 'namespaces'} together —
          a ninth colour would say less than one honest total.
        </p>
      ) : null}
    </div>
  )
}

/*
 * squarify is Bruls, Huizing and van Wijk's algorithm, at the size this chart
 * needs it.
 *
 * It lays tiles into a shrinking rectangle, adding each to the current row
 * while doing so improves that row's worst aspect ratio and starting a new row
 * when it stops. The result is tiles close to square, which is what makes them
 * labellable and comparable.
 */
function squarify(
  entries: { namespace: string; value: number; slot: number }[],
  width: number,
  height: number,
): Tile[] {
  const total = entries.reduce((sum, entry) => sum + entry.value, 0)
  if (total <= 0) return []

  // Work in area units so a tile's area is its share of the whole.
  const scale = (width * height) / total
  const remaining = entries.map((entry) => ({ ...entry, area: entry.value * scale }))

  const tiles: Tile[] = []
  let x = 0
  let y = 0
  let free = { width, height }

  while (remaining.length > 0) {
    const short = Math.min(free.width, free.height)
    const row: typeof remaining = []
    let rowArea = 0

    // Grow the row while the worst aspect ratio in it keeps improving.
    while (remaining.length > 0) {
      const candidate = remaining[0]
      const nextArea = rowArea + candidate.area
      if (row.length > 0 && worstRatio(row, rowArea, short) <= worstRatio(
        [...row, candidate], nextArea, short)) {
        break
      }
      row.push(candidate)
      rowArea = nextArea
      remaining.shift()
    }

    // Lay the row out along the shorter side, which is what keeps it square.
    const thickness = short > 0 ? rowArea / short : 0
    let offset = 0
    for (const entry of row) {
      const length = rowArea > 0 ? (entry.area / rowArea) * short : 0
      tiles.push({
        namespace: entry.namespace,
        value: entry.value,
        slot: entry.slot,
        x: free.width === short ? x + offset : x,
        y: free.width === short ? y : y + offset,
        width: free.width === short ? length : thickness,
        height: free.width === short ? thickness : length,
      })
      offset += length
    }

    if (free.width === short) {
      y += thickness
      free = { width: free.width, height: free.height - thickness }
    } else {
      x += thickness
      free = { width: free.width - thickness, height: free.height }
    }
    // Floating point can leave a sliver that loops forever.
    if (free.width < 0.01 || free.height < 0.01) break
  }

  return tiles
}

/** worstRatio is the worst aspect ratio in a row laid along `short`. */
function worstRatio(
  row: { area: number }[],
  rowArea: number,
  short: number,
): number {
  if (rowArea <= 0 || short <= 0) return Infinity
  let min = Infinity
  let max = 0
  for (const entry of row) {
    min = Math.min(min, entry.area)
    max = Math.max(max, entry.area)
  }
  const squared = short * short
  return Math.max((squared * max) / (rowArea * rowArea), (rowArea * rowArea) / (squared * min))
}

/* ------------------------------------------------------- reserved vs used --- */

/**
 * One workload's reservation with what it actually spent marked on it.
 *
 * Same grammar as the capacity page's allocation bar, and deliberately so: the
 * fill is what was reserved because the reservation is what costs money, and
 * usage is a tick on the same track because the *distance between the two marks
 * is the finding*. A second fill would read as one bar of an ambiguous length.
 */
export function ReservedVersusUsed({
  label,
  reservedPercent,
  usedPercent,
  detail,
}: {
  label: string
  reservedPercent: number
  usedPercent: number
  detail: string
}) {
  const reserved = Math.min(100, Math.max(0, reservedPercent))
  const used = Math.min(100, Math.max(0, usedPercent))

  return (
    <div className="min-w-0">
      <div
        role="meter"
        aria-label={label}
        aria-valuenow={Math.round(reservedPercent)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={detail}
        className="relative h-1.5 overflow-hidden rounded-full bg-raised"
      >
        <span
          aria-hidden="true"
          className="block h-full rounded-full bg-chart-1"
          style={{ width: `${reserved}%` }}
        />
        {usedPercent > 0 ? (
          <span
            aria-hidden="true"
            className="absolute inset-y-0 w-0.5 bg-fg"
            style={{ left: `calc(${used}% - 1px)` }}
          />
        ) : null}
      </div>
      <p className="mt-1 font-mono text-[11px] text-faint tabular-nums">{detail}</p>
    </div>
  )
}
