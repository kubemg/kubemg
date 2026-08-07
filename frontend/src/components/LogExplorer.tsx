import { useCallback, useEffect, useRef, useState } from 'react'
import { ExternalLink, ScrollText, Search } from 'lucide-react'
import { queryError, queryLogs, unconfigured } from '../api/client'
import type { Cluster, LogEntry, LogQueryResult } from '../api/types'
import { queryRangeLabel } from '../lib/timerange'
import { useTimeRange } from '../state/timerange-context'
import { Button, EmptyState, Notice, TextInput } from './primitives'

/*
 * Searching a cluster's aggregated logs.
 *
 * The pod log view reads live from the pod through the tunnel, which is right
 * while the pod is there — but a pod that has been replaced took its logs with
 * it, and there is no way to ask "which pod logged this" across a namespace.
 * That is what this is for, and it is why the two surfaces stay separate rather
 * than one growing a mode switch: one tails a process, the other searches a
 * record, and they fail differently enough that merging them would make both
 * harder to explain.
 *
 * The filter box sends *text*, not a query. LogsQL and LogQL are different
 * languages and the server writes whichever one the cluster's backend speaks,
 * quoting this as a literal — so an operator can type a brace or a quote and get
 * a search for that character rather than a syntax error or a wider search than
 * they were granted.
 */

/*
 * The window comes from the console's one range control rather than from a
 * picker of its own: a search is read against the same span as the chart above
 * it, and the server resolves the preset so both mean the same instant. What is
 * still this surface's own is the *text* being looked for.
 */

export function LogExplorer({
  cluster,
  namespace,
  pod,
  container,
  onConfigure,
}: {
  cluster: Cluster
  /** Pre-narrows the search. The pod drawer passes its own object in. */
  namespace?: string
  pod?: string
  container?: string
  onConfigure?: () => void
}) {
  const { range } = useTimeRange()
  const [filter, setFilter] = useState('')
  // The text actually searched for. It is separate from the box so that typing
  // does not fire a query per keystroke against someone's log backend.
  const [applied, setApplied] = useState('')
  const [wrap, setWrap] = useState(false)

  const [result, setResult] = useState<LogQueryResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [missing, setMissing] = useState(false)
  /* The same search in the cluster's Grafana, built by the server out of the
     LogsQL it just ran — see MetricsChart for why it is not built here. */
  const [explore, setExplore] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const response = await queryLogs(cluster.id, {
        namespace,
        pod,
        container,
        filter: applied,
        range,
      })
      setResult(response.result)
      setExplore(response.grafana_explore ?? null)
      setError(null)
      setMissing(false)
    } catch (err) {
      setMissing(unconfigured(err))
      setError(queryError(err, 'Could not search the logs for this window.'))
      setResult(null)
      setExplore(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, namespace, pod, container, applied, range])

  useEffect(() => {
    void load()
  }, [load])

  if (missing) {
    return (
      <div className="card p-4">
        <EmptyState
          icon={<ScrollText aria-hidden="true" className="size-5" />}
          title="No logs datasource"
        >
          This cluster ships its logs nowhere KubeMG knows about, so there is no history
          to search — a pod&rsquo;s own log is all there is, and it goes when the pod does.
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

  const entries = result?.entries ?? []

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {/* One row of filters above the results, scoping everything below. */}
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          setApplied(filter)
        }}
      >
        <div className="relative min-w-52 flex-1">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-faint"
          />
          <TextInput
            aria-label="Search the logs"
            placeholder="Find in message…"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            className="pl-8"
          />
        </div>

        <Button type="submit" variant="primary" size="sm" disabled={loading}>
          Search
        </Button>
        <Button type="button" size="sm" onClick={() => setWrap((on) => !on)}>
          {wrap ? 'No wrap' : 'Wrap'}
        </Button>
      </form>

      <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted">
        <span className="font-mono">{entries.length}</span>
        <span>lines</span>
        {/* The window is set in the header now, so the results say which one
            they are — a count with no span is a number without a question. */}
        <span>over {queryRangeLabel(range).toLowerCase()}</span>
        {namespace ? (
          <span>
            in <span className="font-mono text-fg">{namespace}</span>
          </span>
        ) : (
          <span>across every namespace you are granted</span>
        )}
        {applied ? (
          <span>
            matching <span className="font-mono text-fg">{applied}</span>
          </span>
        ) : null}
        {explore ? (
          <a
            href={explore}
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1.5 transition-colors hover:text-fg"
            title="Open this search in the cluster's Grafana"
          >
            <ExternalLink aria-hidden="true" className="size-3.5" />
            Grafana
          </a>
        ) : null}
        {loading ? <span className="ml-auto">Searching…</span> : null}
      </div>

      {error && !missing ? <Notice tone="error">{error}</Notice> : null}

      {result?.limited ? (
        <Notice tone="info">
          This is the newest page of results and there are more inside this window. Narrow
          the range or the search to see further back.
        </Notice>
      ) : null}

      {entries.length > 0 ? (
        <LogLines entries={entries} wrap={wrap} showSource={!pod} />
      ) : null}

      {!loading && entries.length === 0 && !error ? (
        <div className="py-6">
          <p className="text-center text-[13px] text-muted">
            Nothing matched in this window.
          </p>
          <details className="mt-3">
            <summary className="cursor-pointer text-center text-[12px] text-faint">
              What KubeMG asked for
            </summary>
            {/* An empty result is usually a shipper writing different field names
                than the query expects, which is invisible without the query. */}
            <pre className="mt-2 overflow-x-auto rounded-control border border-line bg-sunken p-2.5 font-mono text-[11.5px] text-muted">
              {result?.query}
            </pre>
          </details>
        </div>
      ) : null}
    </div>
  )
}

/**
 * The lines themselves. Monospace, newest first, with the pod that wrote each
 * one — which is the column the live log view has no need for and this one
 * cannot do without, since the whole point is reading across pods.
 */
function LogLines({
  entries,
  wrap,
  showSource,
}: {
  entries: LogEntry[]
  wrap: boolean
  showSource: boolean
}) {
  const scroller = useRef<HTMLDivElement | null>(null)

  // A new search starts at the top, where the newest lines are.
  useEffect(() => {
    scroller.current?.scrollTo({ top: 0 })
  }, [entries])

  return (
    <div
      ref={scroller}
      className="min-h-0 flex-1 overflow-auto rounded-card border border-line bg-sunken"
    >
      <ul className="divide-y divide-line-soft">
        {entries.map((entry, index) => (
          <li
            key={`${entry.at}/${entry.pod ?? ''}/${index}`}
            className="flex gap-3 px-3 py-1.5 font-mono text-[12px] leading-relaxed"
          >
            <span className="shrink-0 text-faint tabular-nums">{clockTime(entry.at)}</span>

            {showSource ? (
              <span
                className="w-40 shrink-0 truncate text-muted"
                title={`${entry.namespace ?? ''}/${entry.pod ?? ''}`}
              >
                {entry.pod || '—'}
              </span>
            ) : null}

            <span
              className={`min-w-0 flex-1 text-fg ${
                wrap ? 'break-words whitespace-pre-wrap' : 'truncate'
              }`}
              title={wrap ? undefined : entry.message}
            >
              {entry.message}
              {entry.truncated ? <span className="text-faint"> … (line truncated)</span> : null}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** clockTime renders just the time — the date is the range picker's job. */
function clockTime(at: string): string {
  const parsed = new Date(at)
  if (Number.isNaN(parsed.getTime())) return '—'
  return parsed.toLocaleTimeString()
}
