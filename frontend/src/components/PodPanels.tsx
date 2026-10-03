import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowDownToLine, Container, Pause, Play, RefreshCw, WrapText } from 'lucide-react'
import {
  errorMessage,
  fetchPodLogs,
  fetchPodMetrics,
  proxyURL,
  readToken,
} from '../api/client'
import type { Cluster, ContainerUsage, Pod, PodContainer, PodUsage } from '../api/types'
import { MetricsChart } from './MetricsChart'
import { Button, Chip, Meter, Notice, Panel, Pill, SearchInput } from './primitives'
import { useLiveTick } from '../lib/live'
import { formatCPU, formatMemory, podLimit, ratio } from '../lib/units'

/*
 * The two pod-specific panels: what a pod is doing, and what it is saying. They
 * are panels rather than a drawer of their own because a pod is not a special
 * kind of object — it is one row in a list like any other, and it opens in the
 * same ResourceDetailDrawer as a Service or a CRD. What makes it different is
 * only that there is more to show for it: live usage against each container's
 * own limits, and a log to follow.
 *
 * Both go through the same audited tunnel as every other read.
 */

/**
 * usePodUsage polls one pod's live consumption while the drawer is open.
 * metrics-server itself only refreshes every 15s or so, so asking more often
 * than that would spend tunnel round trips on the same numbers.
 *
 * A cluster with no metrics-server is not an error: the hook reports it as
 * unavailable and the drawer says so where the bars would be.
 */
const USAGE_POLL_MS = 15_000

function usePodUsage(cluster: Cluster, pod: Pod, enabled: boolean) {
  const [usage, setUsage] = useState<PodUsage | null>(null)
  const [unavailable, setUnavailable] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Which pod the newest sample belongs to, so an answer that lands after the
  // drawer has moved to another one is dropped rather than drawn under its name.
  const active = useRef('')

  const read = useCallback(
    async (quiet = false) => {
      const target = `${cluster.id}/${pod.namespace}/${pod.name}`
      active.current = target
      try {
        const result = await fetchPodMetrics(cluster.id, pod.namespace, pod.name)
        if (active.current !== target) return
        setUsage(result.pod)
        setUnavailable(result.available ? null : (result.reason ?? 'No metrics for this cluster.'))
        setError(null)
      } catch (err) {
        if (active.current !== target) return
        // A background sample that fails keeps the last one: bars that vanish
        // because one poll missed say less than the bars did.
        if (!quiet) setError(errorMessage(err, 'Could not read this pod’s usage.'))
      }
    },
    [cluster.id, pod.namespace, pod.name],
  )

  useEffect(() => {
    if (!enabled) return
    void read()
  }, [enabled, read])

  // Sampling stops behind a hidden tab: a drawer left open on a pod is a tunnel
  // round trip every fifteen seconds for a panel nobody is looking at.
  useLiveTick(useCallback(() => read(true), [read]), {
    interval: USAGE_POLL_MS,
    enabled,
  })

  return { usage, unavailable, error }
}

/**
 * PodOverview is a pod's live state: what it is using, and what each of its
 * containers is. Its scheduling facts — node, IP, ready, restarts — are the
 * drawer's summary card, so they are said once, in the card every kind leads
 * with, rather than in a second grid of their own under it.
 */
export function PodOverview({ cluster, pod }: { cluster: Cluster; pod: Pod }) {
  // A pod that is not running has nothing to sample, and asking would only
  // spend a round trip to be told so.
  const running = pod.phase === 'Running'
  const { usage, unavailable, error } = usePodUsage(cluster, pod, running)

  return (
    <>
      {running ? (
        <Panel
          title="Usage"
          eyebrow="Live"
          description={`What the pod is using now, against its limits — sampled every ${USAGE_POLL_MS / 1000}s.`}
          bodyClassName="flex flex-col gap-3 px-5 py-4"
        >
          {error ? <Notice tone="error">{error}</Notice> : null}
          {unavailable ? <Notice tone="info">{unavailable}</Notice> : null}
          {!usage && !unavailable && !error ? (
            <p className="text-[12.5px] text-muted">Reading usage…</p>
          ) : null}

          {usage ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <Meter
                label="CPU"
                value={formatCPU(usage.cpu_millicores)}
                {...bound(usage.cpu_millicores, podLimit(pod.containers, 'cpu'), formatCPU)}
              />
              <Meter
                label="Memory"
                value={formatMemory(usage.memory_bytes)}
                {...bound(usage.memory_bytes, podLimit(pod.containers, 'memory'), formatMemory)}
              />
            </div>
          ) : null}
        </Panel>
      ) : null}

      {/* History, where the cluster has somewhere to keep it. The meters above
          are a two-minute window — enough to say "this is at its limit" and
          never enough to say "since when", which is the question anyone asks
          next. A cluster with no metrics datasource simply says so here.

          Paired, the way the meters above them are: CPU throttling and a
          working set climbing are the same investigation, and stacked they
          were a scroll apart inside a drawer. Each chart is a card already. */}
      <div className="grid gap-4 xl:grid-cols-2">
        <MetricsChart
          cluster={cluster}
          title="CPU per container"
          metric="pod_cpu"
          namespace={pod.namespace}
          pod={pod.name}
        />
        <MetricsChart
          cluster={cluster}
          title="Memory per container"
          metric="pod_memory"
          namespace={pod.namespace}
          pod={pod.name}
        />
      </div>

      <Panel
        title="Containers"
        eyebrow={String(pod.containers.length)}
        description="Each container's image and state, and what it is using against its own limits."
      >
        <ul className="flex flex-col divide-y divide-line-soft">
          {pod.containers.map((entry) => (
            <li key={entry.name} className="px-5 py-3.5">
              <div className="flex items-center gap-3">
                <span
                  aria-hidden="true"
                  className={`grid size-8 shrink-0 place-items-center rounded-full border border-line-soft bg-surface shadow-deck ${
                    entry.ready ? 'text-ok' : 'text-warn'
                  }`}
                >
                  <Container className="size-4" />
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate font-data text-[13px] font-semibold text-fg">{entry.name}</p>
                  <p className="truncate font-data text-[12px] text-muted" title={entry.image}>
                    {entry.image}
                  </p>
                </div>
                {entry.restarts > 0 ? (
                  <span className="shrink-0 font-data text-[12px] text-warn">
                    {entry.restarts} restarts
                  </span>
                ) : null}
                <Pill tone={entry.ready ? 'ok' : 'warn'}>{entry.state}</Pill>
              </div>
              <ContainerUsageBars
                container={entry}
                usage={usage?.containers.find((sample) => sample.name === entry.name)}
              />
            </li>
          ))}
        </ul>
      </Panel>
    </>
  )
}

/** ContainerUsageBars draws one container's consumption against its own limits. */
function ContainerUsageBars({
  container,
  usage,
}: {
  container: PodContainer
  usage?: ContainerUsage
}) {
  if (!usage) return null

  return (
    <div className="mt-3 ml-11 grid gap-3 sm:grid-cols-2">
      <Meter
        label="CPU"
        value={formatCPU(usage.cpu_millicores)}
        {...bound(usage.cpu_millicores, container.cpu_limit_millicores, formatCPU)}
      />
      <Meter
        label="Memory"
        value={formatMemory(usage.memory_bytes)}
        {...bound(usage.memory_bytes, container.memory_limit_bytes, formatMemory)}
      />
    </div>
  )
}

/**
 * bound pairs a reading with its denominator, or with nothing when the
 * container declares no limit. A container without a limit is genuinely
 * unbounded, and inventing a scale for it would misreport how close to trouble
 * it is.
 */
function bound(used: number, limit: number, format: (value: number) => string) {
  if (limit <= 0) return {}
  return { percent: ratio(used, limit), capacity: format(limit) }
}

/**
 * LogView shows the tail of a container's log, and can follow it live. Following
 * uses the proxy's stream path directly rather than polling — the same one
 * `kubectl logs -f` takes.
 */
export function PodLogView({
  cluster,
  pod,
  container,
}: {
  cluster: Cluster
  pod: Pod
  container: string
}) {
  const [lines, setLines] = useState('')
  const [following, setFollowing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const [wrap, setWrap] = useState(true)
  const [autoScroll, setAutoScroll] = useState(true)
  const bottom = useRef<HTMLDivElement | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setLines(await fetchPodLogs(cluster.id, pod.namespace, pod.name, container))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read the log.'))
    } finally {
      setLoading(false)
    }
  }, [cluster.id, pod.namespace, pod.name, container])

  useEffect(() => {
    if (following) return
    void load()
  }, [load, following])

  // Following opens a streamed response and appends as it arrives. Aborting the
  // fetch is what closes the stream, which the bastion records as a clean end.
  useEffect(() => {
    if (!following) return

    const controller = new AbortController()
    const token = readToken() ?? ''

    async function follow() {
      setError(null)
      try {
        const query = new URLSearchParams({
          follow: 'true',
          timestamps: 'true',
          tailLines: '200',
        })
        if (container) query.set('container', container)

        const response = await fetch(
          proxyURL(
            cluster.id,
            `/api/v1/namespaces/${encodeURIComponent(pod.namespace)}/pods/${encodeURIComponent(pod.name)}/log?${query}`,
          ),
          { headers: { Authorization: `Bearer ${token}` }, signal: controller.signal },
        )
        if (!response.ok || !response.body) {
          const detail = await response.text().catch(() => '')
          throw new Error(detail || `the cluster returned ${response.status}`)
        }

        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        setLines('')
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          const chunk = decoder.decode(value, { stream: true })
          setLines((current) => {
            // Keep the buffer bounded; a chatty container would otherwise grow
            // this node until the tab dies.
            const next = current + chunk
            return next.length > 400_000 ? next.slice(-400_000) : next
          })
        }
      } catch (err) {
        if (controller.signal.aborted) return
        setError(err instanceof Error ? err.message : 'The log stream stopped.')
        setFollowing(false)
      }
    }

    void follow()
    return () => controller.abort()
  }, [following, cluster.id, pod.namespace, pod.name, container])

  // Filtering is a view over the buffer, never a filter on the stream: the
  // whole point of narrowing a live log is being able to widen it again without
  // having lost the lines you were not looking at.
  const { shown, total, matched } = useMemo(() => {
    const all = lines.length > 0 ? lines.split('\n') : []
    const needle = filter.trim().toLowerCase()
    if (needle === '') return { shown: lines, total: all.length, matched: all.length }

    const hits = all.filter((line) => line.toLowerCase().includes(needle))
    return { shown: hits.join('\n'), total: all.length, matched: hits.length }
  }, [lines, filter])

  useEffect(() => {
    if (following && autoScroll) bottom.current?.scrollIntoView({ block: 'end' })
  }, [shown, following, autoScroll])

  const empty = filter.trim() !== '' ? 'No line matches that.' : loading ? 'Reading…' : 'No output.'

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" onClick={() => setFollowing((current) => !current)}>
          {following ? (
            <Pause aria-hidden="true" className="size-3.5" />
          ) : (
            <Play aria-hidden="true" className="size-3.5" />
          )}
          {following ? 'Stop following' : 'Follow'}
        </Button>
        {!following ? (
          <Button type="button" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            Reload
          </Button>
        ) : null}

        <SearchInput
          className="min-w-40 flex-1"
          value={filter}
          onChange={setFilter}
          placeholder="Filter lines…"
          label="Filter log lines"
        />

        {/* Both toggles are chips rather than icon buttons: they are states, and
            a state has to read as a word and not only as a highlight. */}
        <Chip active={wrap} onClick={() => setWrap((current) => !current)}>
          <WrapText aria-hidden="true" className="size-3.5" />
          Wrap
        </Chip>
        <Chip active={autoScroll} onClick={() => setAutoScroll((current) => !current)}>
          <ArrowDownToLine aria-hidden="true" className="size-3.5" />
          Tail
        </Chip>
      </div>

      <div className="flex items-center gap-2 text-[12px] text-muted">
        {following ? (
          <span className="flex items-center gap-2">
            <span aria-hidden="true" className="breathe size-1.5 rounded-full bg-ok" />
            streaming
          </span>
        ) : (
          <span>last 200 lines</span>
        )}
        <span className="ml-auto font-data text-[11.5px] text-faint tabular-nums">
          {filter.trim() !== '' ? `${matched} of ${total} lines` : `${total} lines`}
        </span>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}

      <pre
        className={`max-h-[420px] min-h-[240px] flex-1 overflow-auto rounded-card border border-line bg-sunken px-3 py-2.5 font-mono text-[12px] leading-relaxed text-fg ${
          wrap ? 'whitespace-pre-wrap' : 'whitespace-pre'
        }`}
      >
        {shown || empty}
        <div ref={bottom} />
      </pre>
    </div>
  )
}
