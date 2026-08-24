import { useCallback, useEffect, useState } from 'react'
import { ArrowDown, ArrowUp, Minus, RefreshCw, TrendingUp } from 'lucide-react'
import { compareMetrics, queryError, unconfigured } from '../api/client'
import type { Cluster, CompareResult, CompareRow, MetricKind, MetricUnit } from '../api/types'
import { queryRangeLabel } from '../lib/timerange'
import { formatMetric } from '../lib/units'
import { useTimeRange } from '../state/timerange-context'
import { Button, EmptyState, Notice, Row, Segmented, Table, Td, Th } from './primitives'

/*
 * What is worst right now, and is it worse than it was.
 *
 * A chart answers neither. Reading a rank off forty lines is not reading, and
 * reading a change off it means remembering what an hour ago looked like — so
 * this is a table rather than another chart, and it costs a second query
 * against the window before this one to fill its last column.
 *
 * **Direction is carried by form.** Every delta is an arrow and a figure, which
 * survive greyscale, a projector and a squint. Colour is spent only where a
 * direction has a health meaning, and the *server* is what knows which readings
 * those are: a namespace burning more CPU is a fact, a pod restarting more is a
 * problem. That is the `rise` field, and it is the whole reason the delta column
 * is not simply green-down/red-up like every other dashboard.
 */

/** One entry of the reading switcher. */
export interface ComparisonKind {
  kind: MetricKind
  label: string
}

export function MetricComparison({
  cluster,
  kinds,
  namespace,
  /** Rendered instead of the table when the cluster has no metrics datasource. */
  onConfigure,
}: {
  cluster: Cluster
  kinds: ComparisonKind[]
  namespace?: string
  onConfigure?: () => void
}) {
  const { range } = useTimeRange()
  const [kind, setKind] = useState<MetricKind>(kinds[0]?.kind ?? 'cluster_cpu_by_namespace')
  const [result, setResult] = useState<CompareResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const response = await compareMetrics(cluster.id, kind, { namespace, range })
      setResult(response.result)
      setError(null)
      setMissing(false)
    } catch (err) {
      setMissing(unconfigured(err))
      setError(queryError(err, 'Could not rank this reading for the window.'))
      setResult(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, kind, namespace, range])

  useEffect(() => {
    void load()
  }, [load])

  if (missing) {
    return (
      <div className="card p-4">
        <EmptyState
          icon={<TrendingUp aria-hidden="true" className="size-5" />}
          title="No metrics datasource"
        >
          Ranking a window against the one before it needs a metrics backend to read
          history from. The live meters are all kubemg can show without one.
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

  const rows = result?.rows ?? []

  return (
    <section className="card overflow-hidden">
      <header className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
        <div className="min-w-0">
          <h3 className="text-[13px] font-semibold text-fg">Top {result?.topk ?? 5}</h3>
          <p className="mt-0.5 text-[12px] text-muted">
            {queryRangeLabel(range)}, against the window before it
          </p>
        </div>

        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Segmented
            ariaLabel="Reading"
            value={kind}
            onChange={(next) => setKind(next)}
            options={kinds.map((entry) => ({ value: entry.kind, label: entry.label }))}
          />
          <Button type="button" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            <span className="sr-only">Refresh</span>
          </Button>
        </div>
      </header>

      {error && !missing ? (
        <div className="px-4 py-3">
          <Notice tone="error">{error}</Notice>
        </div>
      ) : null}

      {result?.compare_unavailable ? (
        <div className="px-4 py-3">
          {/* Without this the rows would all read as new, which looks like an
              incident rather than a missing second query. */}
          <Notice tone="warn">
            This window is ranked, but the one before it could not be read, so there is
            nothing to compare against: {result.compare_unavailable}
          </Notice>
        </div>
      ) : null}

      {rows.length > 0 ? (
        <div className={loading ? 'opacity-60 transition-opacity' : 'transition-opacity'}>
          <Table>
            <thead>
              <tr>
                <Th>{result?.legend ?? 'name'}</Th>
                <Th align="right" className="w-28">
                  Now
                </Th>
                <Th align="right" className="hidden w-28 sm:table-cell">
                  Before
                </Th>
                <Th align="right" className="w-32">
                  Change
                </Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <Row key={row.name}>
                  <Td className="truncate font-mono text-[13px] text-fg" title={row.name}>
                    {row.name}
                  </Td>
                  <Td className="text-right font-mono tabular-nums text-fg">
                    {formatMetric(result?.unit ?? 'count', row.current)}
                  </Td>
                  <Td className="hidden text-right font-mono tabular-nums text-muted sm:table-cell">
                    {row.previous === undefined
                      ? '—'
                      : formatMetric(result?.unit ?? 'count', row.previous)}
                  </Td>
                  <Td className="text-right">
                    <Delta
                      row={row}
                      unit={result?.unit ?? 'count'}
                      worseWhenRising={result?.rise === 'worse'}
                    />
                  </Td>
                </Row>
              ))}
            </tbody>
          </Table>
        </div>
      ) : null}

      {!loading && rows.length === 0 && !error ? (
        <div className="px-4 py-8">
          <p className="text-center text-[13px] text-muted">
            Nothing reported this reading in the window.
          </p>
          <details className="mt-3">
            <summary className="cursor-pointer text-center text-[12px] text-faint">
              What kubemg asked for
            </summary>
            {/* An empty table is nearly always a backend that labels its series
                differently, or an exporter that is not installed — neither of
                which is visible without the query. */}
            <pre className="mt-2 overflow-x-auto rounded-control border border-line bg-sunken p-2.5 font-mono text-[11.5px] text-muted">
              {result?.query}
            </pre>
          </details>
        </div>
      ) : null}
    </section>
  )
}

/**
 * The delta cell.
 *
 * An arrow and a figure, always. Colour joins them only when the catalogue said
 * a rise means something got worse — restarts, unready containers, throttling —
 * because on everything else "up" is a fact rather than a verdict, and colouring
 * it would tell an operator a namespace is in trouble for doing its job.
 */
function Delta({
  row,
  unit,
  worseWhenRising,
}: {
  row: CompareRow
  unit: MetricUnit
  worseWhenRising: boolean
}) {
  if (row.previous === undefined) {
    // New in this window. It is deliberately not an increase from zero: nothing
    // and "was quiet" are different things to be told.
    return <span className="text-[12.5px] text-muted">new</span>
  }

  const delta = row.delta ?? 0
  // A reading that moved by less than a tenth of a percent of itself has not
  // moved; drawing an arrow for float noise would make every row look busy.
  const still = delta === 0 || Math.abs(delta) < Math.abs(row.current) * 0.001

  if (still) {
    return (
      <span className="inline-flex items-center justify-end gap-1 text-[12.5px] text-faint">
        <Minus aria-hidden="true" className="size-3.5" />
        no change
      </span>
    )
  }

  const rising = delta > 0
  const Arrow = rising ? ArrowUp : ArrowDown
  const tone = !worseWhenRising ? 'text-muted' : rising ? 'text-danger' : 'text-ok'

  // The percentage is the readable half of a change and the absolute is the
  // true one, so the figure leads and the amount follows it.
  const percent = row.delta_ratio === undefined ? null : Math.round(row.delta_ratio * 100)

  return (
    <span
      className={`inline-flex items-center justify-end gap-1 font-mono text-[12.5px] tabular-nums ${tone}`}
    >
      <Arrow aria-hidden="true" className="size-3.5 shrink-0" />
      <span className="sr-only">{rising ? 'up' : 'down'} </span>
      {percent === null ? formatMetric(unit, Math.abs(delta)) : `${Math.abs(percent)}%`}
    </span>
  )
}
