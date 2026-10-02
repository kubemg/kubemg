import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { ExternalLink, LineChart, RefreshCw } from 'lucide-react'
import type { Cluster, MetricKind, MetricResult, MetricSeries } from '../api/types'
import { formatCPU, formatMemory } from '../lib/units'
import { PLOT_FULL, useMetricsQuery } from '../lib/metrics'
import type { PlotGeometry } from '../lib/metrics'
import { queryRangeLabel } from '../lib/timerange'
import { formatClock, formatInstant } from '../lib/time'
import { Button, EmptyState, Notice } from './primitives'

/*
 * A time-series chart, drawn as SVG against the deck's own tokens.
 *
 * There is no charting library here on purpose: the two things drawn are lines
 * and a crosshair, and the smallest chart library worth having is heavier than
 * the terminal emulator that is already lazy-loaded to keep it out of the
 * bundle. What this does need is the parts a library would have given away —
 * a legend, a hover readout, keyboard access and a table view — because a chart
 * whose values are only reachable by hovering is a chart half the readers
 * cannot use.
 *
 * The colour rule: series colours come from the deck's eight chart tokens, in
 * order, never cycled. They are a validated set (see index.css) and they are not
 * the semantic four — a container is not amber for being third in the legend.
 * Identity never rests on colour alone, so every series is written out in the
 * legend beside its key.
 *
 * The look follows the console's dashboards: each line sits on an area that
 * fades from its own colour to nothing, gridlines are dashed and recessive,
 * the latest sample of every series is marked, and the axis is read in the
 * data face at a size somebody can actually read.
 */

/*
 * The window is not this chart's to choose. It comes from the console's one
 * range control in the header (`state/timerange-context.ts`) and travels to the
 * server as a preset id, so two charts side by side cannot disagree about what
 * "now" covers and neither of them computes a boundary the trail would compute
 * differently. What stays local is the refresh button: re-reading *this* chart
 * is about this chart.
 */

/**
 * The eight chart slots, as the class names Tailwind will actually emit. They
 * are literals rather than an interpolation because Tailwind reads the source
 * for class names, and a template string compiles to a rule that does not exist.
 */
const SERIES_STROKE = [
  'text-chart-1',
  'text-chart-2',
  'text-chart-3',
  'text-chart-4',
  'text-chart-5',
  'text-chart-6',
  'text-chart-7',
  'text-chart-8',
] as const

/** The ninth series and beyond fold into one line rather than inventing a hue. */
const MAX_SERIES = SERIES_STROKE.length

export function MetricsChart({
  cluster,
  title,
  metric,
  namespace,
  pod,
  /** Rendered instead of the chart when the cluster has no metrics datasource. */
  onConfigure,
}: {
  cluster: Cluster
  title: string
  metric: MetricKind
  namespace?: string
  pod?: string
  onConfigure?: () => void
}) {
  const { result, loading, error, missing, explore, range, reload: load } = useMetricsQuery({
    cluster,
    metric,
    namespace,
    pod,
  })
  const [showTable, setShowTable] = useState(false)

  if (missing) {
    return (
      <div className="card p-4">
        <EmptyState
          icon={<LineChart aria-hidden="true" className="size-5" />}
          title="No metrics datasource"
        >
          This cluster has no metrics backend registered, so there is no history to
          read — the live meters are all kubemg can show.
          {onConfigure ? (
            <span className="mt-3 block">
              <Button type="button" onClick={onConfigure}>
                Configure a datasource
              </Button>
            </span>
          ) : null}
        </EmptyState>
      </div>
    )
  }

  const series = result?.series ?? []
  const empty = !loading && series.every((entry) => entry.points.length === 0)

  return (
    <div className="card flex min-w-0 flex-col gap-3 px-5 pt-4 pb-4">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0">
          <h3 className="text-[16px] font-bold text-fg">{title}</h3>
          {result?.description ? (
            <p className="mt-0.5 text-[13px] text-muted">{result.description}</p>
          ) : null}
        </div>

        <div className="ml-auto flex items-center gap-2">
          <span className="rounded-full border border-line px-2.5 py-0.5 text-[12px] text-muted">
            {queryRangeLabel(range)}
          </span>
          {/* Where the question outgrows the catalogue. It carries this query
              and this window, so the next question starts where this one
              stopped rather than on Grafana's front page. */}
          {explore ? (
            <a
              href={explore}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1.5 text-[12.5px] text-muted transition-colors duration-300 hover:text-accent"
              title="Open this query in the cluster's Grafana"
            >
              <ExternalLink aria-hidden="true" className="size-3.5" />
              Grafana
            </a>
          ) : null}
          <Button
            type="button"
            size="sm"
            onClick={() => void load()}
            disabled={loading}
            className="size-8 rounded-full px-0"
            title="Refresh"
          >
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            <span className="sr-only">Refresh</span>
          </Button>
        </div>
      </div>

      {error && !missing ? <Notice tone="error">{error}</Notice> : null}

      {result && !empty ? (
        <>
          {/* Refetching holds the previous render at reduced opacity rather than
              collapsing to a skeleton — no layout jump between windows. */}
          <Legend series={series} unit={result.unit} />
          <div className={loading ? 'opacity-60 transition-opacity' : 'transition-opacity'}>
            <Plot result={result} />
          </div>

          {result.truncated ? (
            <p className="text-[12px] text-warn">
              Only the first {MAX_SERIES} series are drawn — this query matched more than a
              chart can show.
            </p>
          ) : null}

          {/* The table is how every value stays reachable without a pointer,
              which is what lets the light deck's lower-contrast slots be legal. */}
          <div>
            <button
              type="button"
              aria-expanded={showTable}
              onClick={() => setShowTable((open) => !open)}
              className="text-[12.5px] font-medium text-muted underline-offset-2 transition-colors duration-300 hover:text-accent hover:underline"
            >
              {showTable ? 'Hide the numbers' : 'Show the numbers'}
            </button>
            {showTable ? <SeriesTable result={result} /> : null}
          </div>
        </>
      ) : null}

      {loading && !result ? (
        <p className="py-8 text-center text-[13px] text-muted">Reading the series…</p>
      ) : null}

      {empty ? (
        <div className="py-6">
          <p className="text-center text-[13px] text-muted">
            The datasource answered, but has nothing for this window.
          </p>
          {/* An empty chart is nearly always a backend that labels its series
              differently, and there is no way to see that without the query. */}
          <details className="mt-3">
            <summary className="cursor-pointer text-center text-[12px] text-faint">
              What kubemg asked for
            </summary>
            <pre
              translate="no"
              className="mt-2 overflow-x-auto rounded-control border border-line bg-sunken p-2.5 font-mono text-[11.5px] text-muted"
            >
              {result?.query}
            </pre>
          </details>
        </div>
      ) : null}
    </div>
  )
}

/* ------------------------------------------------------------------ plot --- */

interface Cursor {
  index: number
  x: number
}

/**
 * measuredWidth tracks the plot's rendered width so the viewBox can map 1:1 to
 * CSS pixels.
 *
 * The obvious alternative — a fixed viewBox with `preserveAspectRatio="none"` —
 * needs no observer and is wrong in two visible ways: it stretches the axis text
 * horizontally by whatever the container/viewBox ratio happens to be, and it
 * turns the crosshair's dots into ellipses. `non-scaling-stroke` rescues the
 * line widths and nothing else.
 */
function useMeasuredWidth(fallback: number) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(fallback)

  useEffect(() => {
    const node = ref.current
    if (!node) return

    const observer = new ResizeObserver((entries) => {
      const measured = entries[0]?.contentRect.width ?? 0
      // A hidden panel measures zero; keeping the last good width stops the
      // scales collapsing and the paths becoming NaN.
      if (measured > 0) setWidth(measured)
    })
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  return { ref, width }
}

export function Plot({
  result,
  geometry = PLOT_FULL,
  /** Dropped in the compact band, where the card around it carries the range. */
  axisLabels = true,
}: {
  result: MetricResult
  geometry?: PlotGeometry
  axisLabels?: boolean
}) {
  const titleId = useId()
  const [cursor, setCursor] = useState<Cursor | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const { ref: frameRef, width } = useMeasuredWidth(720)

  const series = result.series.slice(0, MAX_SERIES)

  // Every series shares one X axis built from the union of their timestamps, so
  // a gap in one line is a gap rather than a shifted line.
  const { times, max } = useMemo(() => {
    const stamps = new Set<number>()
    let peak = 0
    for (const entry of series) {
      for (const point of entry.points) {
        stamps.add(new Date(point.at).getTime())
        if (point.value > peak) peak = point.value
      }
    }
    return { times: [...stamps].sort((a, b) => a - b), max: peak }
  }, [series])

  // The axis gets a compact form; the full one belongs in the readout and the
  // table, where there is room for it.
  const tick = result.unit === 'millicores' ? tickCPU : formatMemory

  if (times.length === 0) return null

  // A flat-zero series still needs a scale, or every point lands on the axis.
  const ceiling = max > 0 ? max * 1.1 : 1
  const spanX = times[times.length - 1] - times[0] || 1

  const plotWidth = width - geometry.left - geometry.right
  const plotHeight = geometry.height - geometry.top - geometry.bottom

  const xFor = (at: number) => geometry.left + ((at - times[0]) / spanX) * plotWidth
  const yFor = (value: number) => geometry.top + plotHeight - (value / ceiling) * plotHeight

  // Four ticks is enough to read a magnitude and few enough to stay recessive.
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((fraction) => fraction * ceiling)

  // Five times along the bottom, the date written only when the window spans
  // more than a day — a clock alone is ambiguous on a seven-day chart.
  const clock = spanX > 24 * 3_600_000 ? DAY_CLOCK : CLOCK
  const timeTicks = [0, 0.25, 0.5, 0.75, 1].map((fraction) => times[0] + fraction * spanX)

  // An area under every line fades from the series' colour to nothing. With
  // more than two lines the fills are kept faint so they do not go muddy where
  // they overlap.
  const areaOpacity = series.length === 1 ? 0.28 : series.length === 2 ? 0.16 : 0.07
  const baseline = yFor(0)

  function locate(event: React.PointerEvent<SVGSVGElement> | React.FocusEvent<SVGSVGElement>) {
    const svg = svgRef.current
    if (!svg) return
    const box = svg.getBoundingClientRect()
    const clientX = 'clientX' in event ? event.clientX : box.left + box.width / 2
    // Back out of the rendered width into viewBox units.
    // The viewBox maps 1:1 to CSS pixels, so a client offset is already a
    // viewBox offset — no ratio to back out of.
    const local = clientX - box.left
    const at = times[0] + ((local - geometry.left) / plotWidth) * spanX

    // Snap to the nearest sample: a reader aims at a time, never at a 2px line.
    let nearest = 0
    for (let i = 1; i < times.length; i += 1) {
      if (Math.abs(times[i] - at) < Math.abs(times[nearest] - at)) nearest = i
    }
    setCursor({ index: nearest, x: xFor(times[nearest]) })
  }

  function onKey(event: React.KeyboardEvent<SVGSVGElement>) {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    const step = event.key === 'ArrowLeft' ? -1 : 1
    const current = cursor?.index ?? times.length - 1
    const next = Math.min(times.length - 1, Math.max(0, current + step))
    setCursor({ index: next, x: xFor(times[next]) })
  }

  return (
    <div ref={frameRef} className="relative w-full">
      <svg
        ref={svgRef}
        role="img"
        aria-labelledby={titleId}
        tabIndex={0}
        viewBox={`0 0 ${width} ${geometry.height}`}
        width={width}
        height={geometry.height}
        className="block max-w-full touch-pan-y rounded-control"
        onPointerMove={locate}
        onPointerLeave={() => setCursor(null)}
        onFocus={locate}
        onBlur={() => setCursor(null)}
        onKeyDown={onKey}
      >
        <title id={titleId}>
          {series.length} series between {formatInstant(result.start)} and{' '}
          {formatInstant(result.end)}. Use the left and right arrow keys to read a sample.
        </title>

        {/* One fade per series slot. A gradient stop's `currentColor` is the
            gradient's own colour, so the slot class goes on the gradient. */}
        <defs>
          {series.map((entry, index) => (
            <linearGradient
              key={`fade-${entry.name}`}
              id={`${titleId}-fade-${index}`}
              x1="0"
              x2="0"
              y1="0"
              y2="1"
              className={SERIES_STROKE[index]}
            >
              <stop offset="0%" stopColor="currentColor" stopOpacity={areaOpacity} />
              <stop offset="100%" stopColor="currentColor" stopOpacity={0} />
            </linearGradient>
          ))}
        </defs>

        {/* Gridlines dashed and one step off the surface; the baseline solid. */}
        {ticks.map((value, index) => (
          <line
            key={value}
            x1={geometry.left}
            x2={width - geometry.right}
            y1={yFor(value)}
            y2={yFor(value)}
            className={index === 0 ? 'stroke-line' : 'stroke-line-soft'}
            strokeWidth={1}
            strokeDasharray={index === 0 ? undefined : '3 4'}
            vectorEffect="non-scaling-stroke"
          />
        ))}

        {ticks.map((value) => (
          <text
            key={`label-${value}`}
            x={geometry.left - 8}
            y={yFor(value) + 3.5}
            textAnchor="end"
            className="fill-faint font-mono text-[10.5px] tabular-nums"
          >
            {tick(value)}
          </text>
        ))}

        {series.map((entry, index) => (
          <path
            key={`area-${entry.name}`}
            d={areaFor(entry, xFor, yFor, baseline)}
            fill={`url(#${titleId}-fade-${index})`}
            stroke="none"
          />
        ))}

        {series.map((entry, index) => (
          <path
            key={entry.name}
            d={pathFor(entry, xFor, yFor)}
            fill="none"
            strokeWidth={series.length === 1 ? 2.5 : 2}
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
            className={`${SERIES_STROKE[index]} stroke-current`}
          />
        ))}

        {/* Every series' latest sample, marked: where the line is now. */}
        {!cursor
          ? series.map((entry, index) => {
              const last = entry.points[entry.points.length - 1]
              if (!last) return null
              const cx = xFor(new Date(last.at).getTime())
              const cy = yFor(last.value)
              return (
                <g key={`end-${entry.name}`} className={SERIES_STROKE[index]}>
                  <circle cx={cx} cy={cy} r={8} className="fill-current" opacity={0.15} />
                  <circle
                    cx={cx}
                    cy={cy}
                    r={4}
                    className="fill-current stroke-surface"
                    strokeWidth={2}
                    vectorEffect="non-scaling-stroke"
                  />
                </g>
              )
            })
          : null}

        {cursor ? (
          <line
            x1={cursor.x}
            x2={cursor.x}
            y1={geometry.top}
            y2={geometry.top + plotHeight}
            className="stroke-faint"
            strokeWidth={1}
            strokeDasharray="2 3"
            vectorEffect="non-scaling-stroke"
          />
        ) : null}

        {/* The endpoints of the crosshair's readout, ringed in the surface colour
            so they stay legible where two lines cross. */}
        {cursor
          ? series.map((entry, index) => {
              const point = entry.points.find(
                (candidate) => new Date(candidate.at).getTime() === times[cursor.index],
              )
              if (!point) return null
              return (
                <circle
                  key={`dot-${entry.name}`}
                  cx={cursor.x}
                  cy={yFor(point.value)}
                  r={4}
                  className={`${SERIES_STROKE[index]} fill-current stroke-surface`}
                  strokeWidth={2}
                  vectorEffect="non-scaling-stroke"
                />
              )
            })
          : null}

        {axisLabels
          ? timeTicks.map((at, index) => (
              <text
                key={`time-${at}`}
                x={xFor(at)}
                y={geometry.height - 7}
                textAnchor={index === 0 ? 'start' : index === timeTicks.length - 1 ? 'end' : 'middle'}
                className="fill-faint font-mono text-[10.5px] tabular-nums"
              >
                {clock.format(at)}
              </text>
            ))
          : null}
      </svg>

      {cursor ? (
        <Readout result={result} series={series} times={times} cursor={cursor} width={width} />
      ) : null}
    </div>
  )
}

/**
 * tickCPU is formatCPU with the word dropped. An axis label competes for the
 * gutter with four others and is read as a magnitude, not as prose — "1.5" under
 * a chart titled "CPU" is unambiguous, while "1.50 cores" is ten characters that
 * would either widen the gutter or be clipped by it.
 */
function tickCPU(millicores: number): string {
  if (!Number.isFinite(millicores) || millicores <= 0) return '0'
  if (millicores < 1000) return `${Math.round(millicores)}m`
  const cores = millicores / 1000
  return cores < 10 ? cores.toFixed(1) : String(Math.round(cores))
}

const CLOCK = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })
const DAY_CLOCK = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
})

/** areaFor is a series' line closed down to the baseline, for its fade. */
function areaFor(
  entry: MetricSeries,
  xFor: (at: number) => number,
  yFor: (value: number) => number,
  baseline: number,
): string {
  if (entry.points.length === 0) return ''
  const first = xFor(new Date(entry.points[0].at).getTime())
  const last = xFor(new Date(entry.points[entry.points.length - 1].at).getTime())
  return `${pathFor(entry, xFor, yFor)} L${last.toFixed(1)} ${baseline.toFixed(1)} L${first.toFixed(1)} ${baseline.toFixed(1)} Z`
}

/** pathFor draws one series, breaking the line where the series has no sample. */
function pathFor(
  entry: MetricSeries,
  xFor: (at: number) => number,
  yFor: (value: number) => number,
): string {
  let path = ''
  let open = false
  for (const point of entry.points) {
    const x = xFor(new Date(point.at).getTime())
    const y = yFor(point.value)
    path += `${open ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)} `
    open = true
  }
  return path.trim()
}

/**
 * One tooltip listing every series at the cursor's time — the pointer never has
 * to land on a line to get a value. Values lead and names follow, which is the
 * legend's hierarchy inverted: here the reader has the series and wants the
 * number.
 */
function Readout({
  result,
  series,
  times,
  cursor,
  width,
}: {
  result: MetricResult
  series: MetricSeries[]
  times: number[]
  cursor: Cursor
  width: number
}) {
  const format = result.unit === 'millicores' ? formatCPU : formatMemory
  const at = times[cursor.index]

  const rows = series
    .map((entry, index) => ({
      name: entry.name,
      slot: index,
      point: entry.points.find((candidate) => new Date(candidate.at).getTime() === at),
    }))
    .filter((row) => row.point)

  if (rows.length === 0) return null

  // The readout follows the cursor but flips to the other side near the right
  // edge, so it never leaves the card.
  const rightHalf = cursor.x > width / 2

  return (
    <div
      aria-live="polite"
      className={`pointer-events-none absolute top-2 z-10 min-w-48 rounded-card border border-line bg-surface px-3.5 py-3 shadow-lift ${
        rightHalf ? 'left-2' : 'right-2'
      }`}
    >
      <p className="mb-2 text-[12px] font-semibold text-fg">{formatClock(at, { seconds: true })}</p>
      <ul className="flex flex-col gap-1.5">
        {rows.map((row) => (
          <li key={row.name} className="flex items-center gap-2">
            <span
              aria-hidden="true"
              className={`size-2 shrink-0 rounded-full ${SERIES_STROKE[row.slot]} bg-current`}
            />
            <span className="min-w-0 truncate font-mono text-[12px] text-muted" translate="no">
              {row.name}
            </span>
            <span className="ml-auto pl-3 font-mono text-[12.5px] font-bold text-fg tabular-nums">
              {format(row.point!.value)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/**
 * The legend. Always present for two or more series, because identity must never
 * rest on colour alone — and on the light deck three of the eight slots sit below
 * 3:1 against white, which the written name is the relief for.
 */
function Legend({ series, unit }: { series: MetricSeries[]; unit: MetricResult['unit'] }) {
  const shown = series.slice(0, MAX_SERIES)
  if (shown.length === 0) return null
  const format = unit === 'millicores' ? formatCPU : formatMemory

  // Each name with its latest reading — the number a reader looks for first.
  return (
    <ul className="flex flex-wrap items-center gap-x-5 gap-y-2">
      {shown.map((entry, index) => {
        const last = entry.points[entry.points.length - 1]
        return (
          <li key={entry.name} className="flex min-w-0 items-center gap-2">
            <span
              aria-hidden="true"
              className={`size-2.5 shrink-0 rounded-full ${SERIES_STROKE[index]} bg-current`}
            />
            <span
              className="max-w-56 truncate font-mono text-[12px] text-muted"
              translate="no"
              title={entry.name}
            >
              {entry.name}
            </span>
            {last ? (
              <span className="font-mono text-[12.5px] font-bold text-fg tabular-nums">
                {format(last.value)}
              </span>
            ) : null}
          </li>
        )
      })}
    </ul>
  )
}

/**
 * The table view. Every value a tooltip shows is reachable without a pointer,
 * which is both the accessibility floor and what makes the lower-contrast light
 * slots legal.
 */
function SeriesTable({ result }: { result: MetricResult }) {
  const format = result.unit === 'millicores' ? formatCPU : formatMemory

  return (
    <div className="mt-2 overflow-x-auto rounded-card border border-line">
      <table className="w-full border-collapse">
        <thead>
          <tr>
            <th className="label px-3 py-2 text-left">Series</th>
            <th className="label px-3 py-2 text-right">Latest</th>
            <th className="label px-3 py-2 text-right">Peak</th>
            <th className="label px-3 py-2 text-right">Mean</th>
          </tr>
        </thead>
        <tbody>
          {result.series.slice(0, MAX_SERIES).map((entry) => {
            const values = entry.points.map((point) => point.value)
            const latest = values.length > 0 ? values[values.length - 1] : 0
            const peak = values.length > 0 ? Math.max(...values) : 0
            const mean =
              values.length > 0 ? values.reduce((sum, value) => sum + value, 0) / values.length : 0
            return (
              <tr key={entry.name} className="border-t border-line-soft">
                <td
                  className="max-w-64 truncate px-3 py-1.5 font-mono text-[12.5px] text-fg"
                  translate="no"
                >
                  {entry.name}
                </td>
                <td className="px-3 py-1.5 text-right font-mono text-[12.5px] text-fg">
                  {format(latest)}
                </td>
                <td className="px-3 py-1.5 text-right font-mono text-[12.5px] text-muted">
                  {format(peak)}
                </td>
                <td className="px-3 py-1.5 text-right font-mono text-[12.5px] text-muted">
                  {format(mean)}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
