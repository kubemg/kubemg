import { useCallback, useEffect, useState } from 'react'
import { queryError, queryMetrics, unconfigured } from '../api/client'
import type { Cluster, MetricKind, MetricResult } from '../api/types'
import { useTimeRange } from '../state/timerange-context'

/*
 * Reading the metric catalogue, and the geometry a reading is drawn at.
 *
 * These live beside the chart rather than inside it because there is now more
 * than one way to *draw* a series — the full card in `MetricsChart` and the
 * compact band in `InsightTrend` — and exactly one way a series may be *read*.
 * A browser never writes a query here: the caller names a chart from a fixed
 * server-side catalogue and the server writes the PromQL around a scope resolved
 * from that caller's grant, because a metrics backend has never heard of the
 * caller and would answer whatever it was asked. One hook is what keeps that
 * from becoming two read paths that can drift.
 */

/**
 * The plot's dimensions. `left` is the axis gutter and has to fit the widest tick
 * label: at 9px JetBrains Mono a character is ~5.4px, and "1.50 cores" is ten of
 * them — so a 52px gutter would clip it off the left edge. The tick formatter
 * keeps labels short and this leaves room for the longest one it can still
 * produce.
 */
export interface PlotGeometry {
  height: number
  left: number
  right: number
  top: number
  bottom: number
}

export const PLOT_FULL: PlotGeometry = { height: 180, left: 62, right: 12, top: 10, bottom: 22 }

/**
 * The band inside the Explore pilot header. Shorter, and with a tighter gutter
 * because it is read as a shape rather than off its axis — the number that
 * matters is written out above it in full, so the ticks here only have to carry
 * a magnitude.
 */
export const PLOT_COMPACT: PlotGeometry = { height: 104, left: 46, right: 8, top: 8, bottom: 16 }

/** One reading of the catalogue, as state. See the note at the top of the file. */
export function useMetricsQuery({
  cluster,
  metric,
  namespace,
  pod,
}: {
  cluster: Cluster
  metric: MetricKind
  namespace?: string
  pod?: string
}) {
  const { range } = useTimeRange()
  const [result, setResult] = useState<MetricResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState(false)
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

  return { result, loading, error, missing, explore, range, reload: load }
}
