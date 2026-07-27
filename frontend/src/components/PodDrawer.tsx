import { Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react'
import { Pause, Play, RefreshCw } from 'lucide-react'
import { errorMessage, fetchPodLogs, proxyURL, readToken } from '../api/client'
import type { Cluster, Pod } from '../api/types'
import { Button, DetailList, Notice, Pill, Segmented, Select, Sheet } from './primitives'
import { podTone } from '../lib/status'
import { relativeAge } from '../lib/time'

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

      {tab === 'overview' ? <Overview pod={pod} /> : null}
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

function Overview({ pod }: { pod: Pod }) {
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
            </li>
          ))}
        </ul>
      </div>
    </>
  )
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

  useEffect(() => {
    if (following) bottom.current?.scrollIntoView({ block: 'end' })
  }, [lines, following])

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2.5">
      <div className="flex items-center gap-2">
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
        <span className="ml-auto flex items-center gap-2 text-[12px] text-muted">
          {following ? (
            <>
              <span aria-hidden="true" className="breathe size-1.5 rounded-full bg-ok" />
              streaming
            </>
          ) : (
            'last 200 lines'
          )}
        </span>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}

      <pre className="max-h-[420px] min-h-[240px] flex-1 overflow-auto rounded-card border border-line bg-sunken px-3 py-2.5 font-mono text-[12px] leading-relaxed whitespace-pre-wrap text-fg">
        {lines || (loading ? 'Reading…' : 'No output.')}
        <div ref={bottom} />
      </pre>
    </div>
  )
}
