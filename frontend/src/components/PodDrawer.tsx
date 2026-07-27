import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowDownToLine, Pause, Play, RefreshCw, WrapText } from 'lucide-react'
import {
  errorMessage,
  fetchPodLogs,
  fetchPodMetrics,
  proxyURL,
  readToken,
} from '../api/client'
import type { Cluster, ContainerUsage, Pod, PodContainer, PodUsage } from '../api/types'
import {
  Button,
  Chip,
  DetailList,
  Meter,
  Notice,
  Pill,
  SearchInput,
  Segmented,
  Select,
  Sheet,
} from './primitives'
import { podTone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory, ratio } from '../lib/units'

// The terminal emulator is by far the heaviest thing in the app and most sessions
// never open one, so it is fetched on demand rather than shipped to everyone who
// loads the console.
const PodTerminal = lazy(() =>
  import('./PodTerminal').then((module) => ({ default: module.PodTerminal })),
)

type Tab = 'overview' | 'logs' | 'terminal'

/**
 * PodDrawer is everything you can do to one pod without leaving the list: read
 * its state, follow its logs, or open a shell in it. All three go through the
 * same audited tunnel.
 */
export function PodDrawer({
  cluster,
  pod,
  onClose,
  onRefresh,
}: {
  cluster: Cluster
  pod: Pod
  onClose: () => void
  onRefresh: () => Promise<void> | void
}) {
  const [tab, setTab] = useState<Tab>('overview')
  const [container, setContainer] = useState(pod.containers[0]?.name ?? '')

  return (
    <Sheet
      width="lg"
      eyebrow={`${cluster.name} · ${pod.namespace}`}
      title={<span className="font-mono">{pod.name}</span>}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button type="button" onClick={() => void onRefresh()}>
            <RefreshCw aria-hidden="true" className="size-4" />
            Refresh
          </Button>
        </>
      }
    >
      <div className="flex flex-wrap items-center gap-3">
        <Segmented<Tab>
          ariaLabel="Pod view"
          value={tab}
          onChange={setTab}
          options={[
            { value: 'overview', label: 'Overview' },
            { value: 'logs', label: 'Logs' },
            { value: 'terminal', label: 'Terminal' },
          ]}
        />
        <Pill tone={podTone(pod)}>{pod.phase}</Pill>

        {pod.containers.length > 1 && tab !== 'overview' ? (
          <div className="ml-auto w-44">
            <Select
              aria-label="Container"
              size="sm"
              value={container}
              onChange={(event) => setContainer(event.target.value)}
            >
              {pod.containers.map((entry) => (
                <option key={entry.name} value={entry.name}>
                  {entry.name}
                </option>
              ))}
            </Select>
          </div>
        ) : null}
      </div>

      {tab === 'overview' ? <Overview cluster={cluster} pod={pod} /> : null}
      {tab === 'logs' ? <LogView cluster={cluster} pod={pod} container={container} /> : null}
      {tab === 'terminal' ? (
        <Suspense fallback={<p className="text-[13px] text-muted">Loading the terminal…</p>}>
          <PodTerminal
            clusterId={cluster.id}
            namespace={pod.namespace}
            pod={pod.name}
            container={container}
          />
        </Suspense>
      ) : null}
    </Sheet>
  )
}

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

  useEffect(() => {
    if (!enabled) return

    let live = true
    async function read() {
      try {
        const result = await fetchPodMetrics(cluster.id, pod.namespace, pod.name)
        if (!live) return
        setUsage(result.pod)
        setUnavailable(result.available ? null : (result.reason ?? 'No metrics for this cluster.'))
        setError(null)
      } catch (err) {
        if (!live) return
        setError(errorMessage(err, 'Could not read this pod’s usage.'))
      }
    }

    void read()
    const timer = window.setInterval(() => void read(), USAGE_POLL_MS)
    return () => {
      live = false
      window.clearInterval(timer)
    }
  }, [cluster.id, pod.namespace, pod.name, enabled])

  return { usage, unavailable, error }
}

function Overview({ cluster, pod }: { cluster: Cluster; pod: Pod }) {
  // A pod that is not running has nothing to sample, and asking would only
  // spend a round trip to be told so.
  const running = pod.phase === 'Running'
  const { usage, unavailable, error } = usePodUsage(cluster, pod, running)

  return (
    <>
      <DetailList
        columns={2}
        rows={[
          { term: 'Namespace', value: pod.namespace },
          { term: 'Node', value: pod.node || 'unscheduled' },
          { term: 'Pod IP', value: pod.pod_ip || '—' },
          { term: 'Age', value: relativeAge(pod.created_at) },
          { term: 'Ready', value: `${pod.ready}/${pod.total}` },
          {
            term: 'Restarts',
            value: String(pod.restarts),
            tone: pod.restarts > 0 ? 'warn' : 'default',
          },
        ]}
      />

      {running ? (
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <span className="label">Usage</span>
            {usage ? (
              <span className="flex items-center gap-1.5 text-[11px] text-faint">
                <span aria-hidden="true" className="size-1 rounded-full bg-ok" />
                sampled every {USAGE_POLL_MS / 1000}s
              </span>
            ) : null}
          </div>

          {error ? <Notice tone="error">{error}</Notice> : null}
          {unavailable ? <Notice tone="info">{unavailable}</Notice> : null}
          {!usage && !unavailable && !error ? (
            <p className="text-[12.5px] text-muted">Reading usage…</p>
          ) : null}

          {usage ? (
            <div className="grid gap-4 rounded-card border border-line px-3 py-3 sm:grid-cols-2">
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
        </div>
      ) : null}

      <div className="flex flex-col gap-2">
        <span className="label">Containers</span>
        <ul className="flex flex-col gap-2">
          {pod.containers.map((entry) => (
            <li key={entry.name} className="rounded-card border border-line px-3 py-2.5">
              <div className="flex items-center gap-2.5">
                <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-fg">
                  {entry.name}
                </span>
                {entry.restarts > 0 ? (
                  <span className="font-mono text-[12px] text-warn">
                    {entry.restarts} restarts
                  </span>
                ) : null}
                <Pill tone={entry.ready ? 'ok' : 'warn'}>{entry.state}</Pill>
              </div>
              <p className="mt-1 truncate font-mono text-[12px] text-muted" title={entry.image}>
                {entry.image}
              </p>
              <ContainerUsageBars
                container={entry}
                usage={usage?.containers.find((sample) => sample.name === entry.name)}
              />
            </li>
          ))}
        </ul>
      </div>
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
    <div className="mt-2.5 grid gap-3 border-t border-line-soft pt-2.5 sm:grid-cols-2">
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
 * podLimit sums a pod's container limits. A pod is only bounded if *every*
 * container is: one unlimited container makes the pod unlimited, so a total
 * across the rest would be a ceiling that does not exist.
 */
function podLimit(containers: PodContainer[], resource: 'cpu' | 'memory'): number {
  let total = 0
  for (const container of containers) {
    const limit =
      resource === 'cpu' ? container.cpu_limit_millicores : container.memory_limit_bytes
    if (limit <= 0) return 0
    total += limit
  }
  return total
}

/**
 * LogView shows the tail of a container's log, and can follow it live. Following
 * uses the proxy's stream path directly rather than polling — the same one
 * `kubectl logs -f` takes.
 */
function LogView({ cluster, pod, container }: { cluster: Cluster; pod: Pod; container: string }) {
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
          placeholder="Filter lines"
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
        <span className="ml-auto font-mono text-[11.5px] text-faint tabular-nums">
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
