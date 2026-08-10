import { queryError, queryMetrics, unconfigured } from '../api/client'
import type { Cluster, MetricKind, MetricQueryResponse } from '../api/types'
import { queryKey, useCachedQuery } from './query'
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

/**
 * One reading of the catalogue, as state. See the note at the top of the file.
 *
 * It goes through the console's read cache, and for this read that matters more
 * than for most. A chart in the Explore pilot header is re-asked by ordinary
 * navigation — a click to Deployments and back, a CPU/MEM toggle and back, a
 * drawer opened over the list — and on an **in-cluster** datasource each of those
 * is not just a query against a metrics backend: it is a call through the agent
 * tunnel to the target cluster's API server, which proxies it to the Service. So
 * a repeat within the window has to cost nothing, and returning to an axis
 * already looked at has to be instant.
 *
 * The key is the whole question: cluster, chart, namespace, pod and the window.
 * The window belongs in it because the same chart over a different range is a
 * different answer — leaving it out is how a cache starts lying about time.
 *
 * Failures are deliberately not cached (`useCachedQuery` only stores what
 * succeeded), which is what keeps "no datasource" from sticking: an operator who
 * registers one is answered by the next read rather than having to wait out an
 * entry.
 */
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
  const key = queryKey('metrics', cluster.id, metric, namespace ?? '-', pod ?? '-', range)

  const query = useCachedQuery<MetricQueryResponse>(key, () =>
    queryMetrics(cluster.id, metric, { namespace, pod, range }),
  )

  return {
    result: query.data?.result ?? null,
    /* The same query in the cluster's own Grafana. It arrives *with* the result
       because the server built it out of the query it just ran — a browser
       assembling its own Explore link would be a browser writing a query. */
    explore: query.data?.grafana_explore ?? null,
    // Anything in flight reads as loading: a caller uses this to dim a chart it
    // is still showing as well as to say it has nothing yet.
    loading: query.loading || query.revalidating,
    // "No datasource yet" is not a failure, it is a setup step — and it reads
    // completely differently on screen.
    missing: unconfigured(query.error),
    error: query.error ? queryError(query.error, 'Could not read metrics for this window.') : null,
    range,
    reload: query.refresh,
  }
}
