/*
 * The trend region of the Explore pilot header: what this namespace has been
 * doing, beside what it currently is.
 *
 * Everything else in that header is derived from rows already loaded and costs
 * no read. This one does not — it is the only part of the band that asks the
 * cluster's datasource a question — so three rules keep it from turning a fast
 * page into a slow one and from making a claim it cannot back up.
 *
 *   1. **It reads history, and history is optional.** Live utilisation in this
 *      product is a *point sample*: metrics-server keeps about two minutes, which
 *      is why every other surface draws meters off it and never a curve. A curve
 *      needs the datasource a cluster registered, and plenty of clusters have
 *      not registered one — so "no datasource" is a first-class state here, not
 *      an error. It is drawn as the shape of a chart behind a blur with the fact
 *      written across it: the region keeps its size so the band does not change
 *      shape between clusters, and the blur is what says *there is a chart here
 *      you cannot have yet* rather than *this namespace is flat*.
 *
 *   2. **It is a namespace reading, and it says so.** The catalogue's namespaced
 *      entries are per-namespace, so this only appears where exactly one
 *      namespace is selected. Under "All namespaces" there is no honest
 *      equivalent — a cluster-wide curve is refused outright to a scoped grant,
 *      and summing every namespace would answer a question nobody asked from
 *      that list — so the band simply stays the simple one.
 *
 *   3. **It never writes a query.** It goes through `useMetricsQuery`, the same
 *      hook the full chart uses, which is the same fixed catalogue entry the
 *      server writes the PromQL for. There is no second read path here to drift.
 */

import { useState } from 'react'
import { ExternalLink, LineChart } from 'lucide-react'
import type { Cluster, MetricKind, MetricResult } from '../api/types'
import { Plot } from './MetricsChart'
import { PLOT_COMPACT, useMetricsQuery } from '../lib/metrics'
import { Segmented } from './primitives'
import { queryRangeLabel } from '../lib/timerange'
import { formatCPU, formatMemory } from '../lib/units'

/** Which of the two readings the region is showing. */
type Axis = 'cpu' | 'memory'

const METRIC: Record<Axis, MetricKind> = {
  cpu: 'namespace_cpu',
  memory: 'namespace_memory',
}

export function InsightTrend({
  cluster,
  namespace,
  /** Opens the datasource editor, where there is one to open. */
  onConfigure,
}: {
  cluster: Cluster
  namespace: string
  onConfigure?: () => void
}) {
  const [axis, setAxis] = useState<Axis>('cpu')
  const { result, loading, error, missing, explore, range } = useMetricsQuery({
    cluster,
    metric: METRIC[axis],
    namespace,
  })

  const format = axis === 'cpu' ? formatCPU : formatMemory
  const reading = result ? total(result) : null
  const empty = Boolean(result) && !reading

  return (
    <div className="flex min-w-0 flex-col gap-2 px-4 py-3.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <p className="label">{axis === 'cpu' ? 'Namespace CPU' : 'Namespace memory'}</p>
        <span className="font-mono text-[11.5px] text-faint">{queryRangeLabel(range)}</span>
        {explore ? (
          <a
            href={explore}
            target="_blank"
            rel="noreferrer noopener"
            title="Open this query in the cluster's Grafana"
            className="inline-flex items-center gap-1 text-[11.5px] text-muted transition-colors hover:text-fg"
          >
            <ExternalLink aria-hidden="true" className="size-3" />
            Grafana
          </a>
        ) : null}
        <div className="ml-auto">
          <Segmented<Axis>
            ariaLabel="Which reading to chart"
            value={axis}
            onChange={setAxis}
            options={[
              { value: 'cpu', label: 'CPU' },
              { value: 'memory', label: 'MEM' },
            ]}
          />
        </div>
      </div>

      {/* The number is written out above the curve rather than left to a hover,
          which is what lets the plot's own gutter stay narrow. */}
      {reading ? (
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
          <span className="font-mono text-[19px] leading-none font-semibold text-fg tabular-nums">
            {format(reading.latest)}
          </span>
          {/* A namespace burning more CPU is a fact, not a fault — the catalogue
              calls these readings neutral — so the delta carries its direction in
              the glyph and spends no state colour on it. */}
          {reading.delta !== null ? (
            <span className="font-mono text-[11.5px] font-semibold text-muted tabular-nums">
              {reading.delta >= 0 ? '▲' : '▼'} {Math.abs(Math.round(reading.delta))}%
            </span>
          ) : null}
          <span className="text-[11.5px] text-faint">
            peak {format(reading.peak)} · {reading.series} series
          </span>
        </div>
      ) : null}

      {missing ? (
        <Unconfigured onConfigure={onConfigure} />
      ) : error ? (
        // A failed read is a sentence, not a Notice: the band is chrome over a
        // table somebody is reading, and a red panel in it would outrank the
        // list it sits above.
        <p className="py-6 text-center text-[12px] text-warn">{error}</p>
      ) : empty ? (
        <p className="py-6 text-center text-[12px] text-muted">
          The datasource answered with nothing for this window.
        </p>
      ) : result ? (
        <div className={loading ? 'opacity-60 transition-opacity' : 'transition-opacity'}>
          <Plot result={result} geometry={PLOT_COMPACT} axisLabels={false} />
        </div>
      ) : (
        <p className="py-6 text-center text-[12px] text-muted">Reading the series…</p>
      )}
    </div>
  )
}

/**
 * What the region looks like on a cluster with no metrics backend: the shape of a
 * chart, blurred past reading, with the fact across it.
 *
 * The curve is a **fixed decorative path** — it is not this namespace's data at
 * any resolution, and it never could be, because there is nothing to read. It is
 * blurred hard, held at low opacity and hidden from assistive technology, so it
 * cannot be mistaken for a rendering of anything; what it does is keep the region
 * the same size and shape it has on a cluster that *is* configured, so the band
 * does not visibly rearrange as an operator moves across a fleet where only some
 * clusters have a datasource.
 */
function Unconfigured({ onConfigure }: { onConfigure?: () => void }) {
  return (
    <div className="relative isolate" style={{ height: PLOT_COMPACT.height }}>
      <svg
        aria-hidden="true"
        viewBox="0 0 300 104"
        preserveAspectRatio="none"
        className="absolute inset-0 size-full text-chart-1 opacity-30 blur-[5px]"
      >
        <path
          d="M0 78 L30 66 L60 71 L90 44 L120 52 L150 30 L180 38 L210 20 L240 33 L270 24 L300 12"
          fill="none"
          stroke="currentColor"
          strokeWidth={3}
          strokeLinejoin="round"
        />
      </svg>

      <div className="absolute inset-0 grid place-items-center px-2">
        <div className="flex flex-col items-center gap-1.5 text-center">
          <span className="inline-flex items-center gap-1.5 rounded-chip border border-line bg-surface/85 px-2.5 py-1 text-[12px] font-medium text-muted">
            <LineChart aria-hidden="true" className="size-3.5" />
            No data source
          </span>
          <span className="text-[11.5px] text-faint">
            This cluster has no metrics backend, so there is no history to read.
          </span>
          {onConfigure ? (
            <button
              type="button"
              onClick={onConfigure}
              className="text-[11.5px] text-accent underline-offset-2 transition-colors hover:underline"
            >
              Register a datasource
            </button>
          ) : null}
        </div>
      </div>
    </div>
  )
}

/**
 * The one number the region leads with, summed across the series the catalogue
 * broke the namespace into — a namespace's CPU is the sum of its pods', and a
 * reader looking at a header wants the namespace, not the busiest pod in it.
 *
 * The delta compares the last sample against the first of the same window rather
 * than against a fixed span, so it always means "across the range in the header"
 * and cannot disagree with the curve drawn under it. It is null where there is
 * only one sample to compare, or where the window opened at zero — a rise from
 * nothing is not a percentage.
 */
function total(result: MetricResult): {
  latest: number
  peak: number
  delta: number | null
  series: number
} | null {
  const stamps = new Map<number, number>()
  let peak = 0

  for (const entry of result.series) {
    for (const point of entry.points) {
      const at = new Date(point.at).getTime()
      const sum = (stamps.get(at) ?? 0) + point.value
      stamps.set(at, sum)
      if (sum > peak) peak = sum
    }
  }

  if (stamps.size === 0) return null

  const ordered = [...stamps].sort((a, b) => a[0] - b[0])
  const first = ordered[0][1]
  const latest = ordered[ordered.length - 1][1]

  return {
    latest,
    peak,
    delta: ordered.length > 1 && first > 0 ? ((latest - first) / first) * 100 : null,
    series: result.series.length,
  }
}
