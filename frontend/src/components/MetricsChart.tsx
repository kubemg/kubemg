import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { ExternalLink, LineChart, RefreshCw } from 'lucide-react'
import { queryError, queryMetrics, unconfigured } from '../api/client'
import type { Cluster, MetricKind, MetricResult, MetricSeries } from '../api/types'
import { formatCPU, formatMemory } from '../lib/units'
import { queryRangeLabel } from '../lib/timerange'
import { useTimeRange } from '../state/timerange-context'
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
  const { range } = useTimeRange()
  const [result, setResult] = useState<MetricResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState(false)
  const [showTable, setShowTable] = useState(false)
  /* The same query in the cluster's own Grafana. It arrives *with* the result
     because the server built it out of the query it just ran — a browser
     assembling its own Explore link would be a browser writing a query. */
  const [explore, setExplore] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const response = await queryMetrics(cluster.id, metric, { namespace, pod, range })
      setResult(response.result)
      setExplore(response.grafana_explore ?? null)
      setError(null)
      setMissing(false)
    } catch (err) {
      // "No datasource yet" is not a failure, it is a setup step — and it reads
      // completely differently on screen.
      setMissing(unconfigured(err))
      setError(queryError(err, 'Could not read metrics for this window.'))
      setResult(null)
      setExplore(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, metric, namespace, pod, range])

  useEffect(() => {
    void load()
  }, [load])

  if (missing) {
    return (
      <div className="card p-4">
        <EmptyState
          icon={<LineChart aria-hidden="true" className="size-5" />}
          title="No metrics datasource"
        >
          This cluster has no metrics backend registered, so there is no history to
          read — the live meters are all KubeMG can show.
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
    <div className="card flex flex-col gap-3 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h3 className="text-[13px] font-semibold text-fg">{title}</h3>

        <div className="ml-auto flex items-center gap-2">
          <span className="text-[12px] text-muted">{queryRangeLabel(range)}</span>
          {/* Where the question outgrows the catalogue. It carries this query
              and this window, so the next question starts where this one
              stopped rather than on Grafana's front page. */}
          {explore ? (
            <a
              href={explore}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1.5 text-[12px] text-muted transition-colors hover:text-fg"
              title="Open this query in the cluster's Grafana"
            >
              <ExternalLink aria-hidden="true" className="size-3.5" />
              Grafana
            </a>
          ) : null}
          <Button type="button" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            <span className="sr-only">Refresh</span>
          </Button>
        </div>
      </div>

      {result?.description ? (
        <p className="text-[12px] text-muted">{result.description}</p>
      ) : null}

      {error && !missing ? <Notice tone="error">{error}</Notice> : null}

      {result && !empty ? (
        <>
          {/* Refetching holds the previous render at reduced opacity rather than
              collapsing to a skeleton — no layout jump between windows. */}
          <div className={loading ? 'opacity-60 transition-opacity' : 'transition-opacity'}>
            <Plot result={result} />
          </div>
          <Legend series={series} />

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
              onClick={() => setShowTable((open) => !open)}
              className="text-[12px] text-muted underline-offset-2 transition-colors hover:text-fg hover:underline"
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
              What KubeMG asked for
            </summary>
            <pre className="mt-2 overflow-x-auto rounded-control border border-line bg-sunken p-2.5 font-mono text-[11.5px] text-muted">
              {result?.query}
            </pre>
          </details>
        </div>
      ) : null}
    </div>
  )
}

/* ------------------------------------------------------------------ plot --- */

/**
 * The plot's fixed dimensions. `left` is the axis gutter and has to fit the
 * widest tick label: at 9px JetBrains Mono a character is ~5.4px, and "1.50
 * cores" is ten of them — so a 52px gutter would clip it off the left edge.
 * The tick formatter keeps labels short and this leaves room for the longest
 * one it can still produce.
 */
const PLOT = { height: 180, left: 62, right: 12, top: 10, bottom: 22 }

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

function Plot({ result }: { result: MetricResult }) {
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

  const plotWidth = width - PLOT.left - PLOT.right
  const plotHeight = PLOT.height - PLOT.top - PLOT.bottom

  const xFor = (at: number) => PLOT.left + ((at - times[0]) / spanX) * plotWidth
  const yFor = (value: number) => PLOT.top + plotHeight - (value / ceiling) * plotHeight

  // Four ticks is enough to read a magnitude and few enough to stay recessive.
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((fraction) => fraction * ceiling)

  function locate(event: React.PointerEvent<SVGSVGElement> | React.FocusEvent<SVGSVGElement>) {
    const svg = svgRef.current
    if (!svg) return
    const box = svg.getBoundingClientRect()
    const clientX = 'clientX' in event ? event.clientX : box.left + box.width / 2
    // Back out of the rendered width into viewBox units.
    // The viewBox maps 1:1 to CSS pixels, so a client offset is already a
    // viewBox offset — no ratio to back out of.
    const local = clientX - box.left
    const at = times[0] + ((local - PLOT.left) / plotWidth) * spanX

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
        viewBox={`0 0 ${width} ${PLOT.height}`}
        width={width}
        height={PLOT.height}
        className="block touch-none rounded-control focus:outline-none focus-visible:ring-1 focus-visible:ring-accent"
        onPointerMove={locate}
        onPointerLeave={() => setCursor(null)}
        onFocus={locate}
        onBlur={() => setCursor(null)}
        onKeyDown={onKey}
      >
        <title id={titleId}>
          {result.series.length} series between {new Date(result.start).toLocaleString()} and{' '}
          {new Date(result.end).toLocaleString()}
        </title>

        {/* Gridlines: hairline, solid, one step off the surface. */}
        {ticks.map((value) => (
          <line
            key={value}
            x1={PLOT.left}
            x2={width - PLOT.right}
            y1={yFor(value)}
            y2={yFor(value)}
            className="stroke-line-soft"
            strokeWidth={1}
            vectorEffect="non-scaling-stroke"
          />
        ))}

        {ticks.map((value) => (
          <text
            key={`label-${value}`}
            x={PLOT.left - 6}
            y={yFor(value) + 3}
            textAnchor="end"
            className="fill-faint font-mono text-[9px]"
          >
            {tick(value)}
          </text>
        ))}

        {series.map((entry, index) => (
          <path
            key={entry.name}
            d={pathFor(entry, xFor, yFor)}
            fill="none"
            strokeWidth={2}
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
            className={`${SERIES_STROKE[index]} stroke-current`}
          />
        ))}

        {cursor ? (
          <line
            x1={cursor.x}
            x2={cursor.x}
            y1={PLOT.top}
            y2={PLOT.top + plotHeight}
            className="stroke-faint"
            strokeWidth={1}
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
                  r={3.5}
                  className={`${SERIES_STROKE[index]} fill-current stroke-surface`}
                  strokeWidth={2}
                  vectorEffect="non-scaling-stroke"
                />
              )
            })
          : null}

        <text
          x={PLOT.left}
          y={PLOT.height - 6}
          className="fill-faint font-mono text-[9px]"
        >
          {new Date(times[0]).toLocaleTimeString()}
        </text>
        <text
          x={width - PLOT.right}
          y={PLOT.height - 6}
          textAnchor="end"
          className="fill-faint font-mono text-[9px]"
        >
          {new Date(times[times.length - 1]).toLocaleTimeString()}
        </text>
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
      className={`pointer-events-none absolute top-2 z-10 min-w-40 rounded-control border border-line bg-surface px-2.5 py-2 lift ${
        rightHalf ? 'left-2' : 'right-2'
      }`}
    >
      <p className="mb-1.5 font-mono text-[11px] text-faint">
        {new Date(at).toLocaleTimeString()}
      </p>
      <ul className="flex flex-col gap-1">
        {rows.map((row) => (
          <li key={row.name} className="flex items-center gap-2">
            {/* A short stroke rather than a filled box: at this density a swatch
                is data-weight ink doing a label's job. */}
            <span
              aria-hidden="true"
              className={`h-0.5 w-3 shrink-0 rounded-full ${SERIES_STROKE[row.slot]} bg-current`}
            />
            <span className="font-mono text-[12px] font-semibold text-fg">
              {format(row.point!.value)}
            </span>
            <span className="ml-auto truncate font-mono text-[11px] text-muted">{row.name}</span>
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
function Legend({ series }: { series: MetricSeries[] }) {
  const shown = series.slice(0, MAX_SERIES)
  if (shown.length < 2) return null

  return (
    <ul className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
      {shown.map((entry, index) => (
        <li key={entry.name} className="flex items-center gap-1.5">
          <span
            aria-hidden="true"
            className={`h-0.5 w-3.5 shrink-0 rounded-full ${SERIES_STROKE[index]} bg-current`}
          />
          <span className="font-mono text-[12px] text-muted">{entry.name}</span>
        </li>
      ))}
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
                <td className="truncate px-3 py-1.5 font-mono text-[12.5px] text-fg">
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
