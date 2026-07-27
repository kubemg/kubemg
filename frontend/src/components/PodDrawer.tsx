import { Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react'
import { Pause, Play, RefreshCw } from 'lucide-react'
import { errorMessage, fetchPodLogs, proxyURL, readToken } from '../api/client'
import type { Cluster, Pod } from '../api/types'
import { Button, Drawer, Notice, Pill, Select } from './primitives'
import { relativeAge } from '../lib/time'

// The terminal emulator is by far the heaviest thing in the app and most
// sessions never open one, so it is fetched on demand rather than shipped to
// everyone who loads the console.
const PodTerminal = lazy(() =>
  import('./PodTerminal').then((module) => ({ default: module.PodTerminal })),
)

type Tab = 'overview' | 'logs' | 'terminal'

const TABS: { id: Tab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'logs', label: 'Logs' },
  { id: 'terminal', label: 'Terminal' },
]

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
    <Drawer
      title={pod.name}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button type="button" onClick={() => void onRefresh()}>
            <RefreshCw aria-hidden="true" className="size-3.5" />
            Refresh
          </Button>
        </>
      }
    >
      <div className="flex items-center gap-1">
        {TABS.map((entry) => (
          <button
            key={entry.id}
            type="button"
            onClick={() => setTab(entry.id)}
            aria-pressed={tab === entry.id}
            className={`rounded-[5px] border px-2.5 py-1 text-[12.5px] transition-colors ${
              tab === entry.id
                ? 'border-primary/40 bg-primary-soft font-medium text-primary'
                : 'border-line bg-surface text-muted hover:text-fg'
            }`}
          >
            {entry.label}
          </button>
        ))}
      </div>

      {pod.containers.length > 1 && tab !== 'overview' ? (
        <Select
          aria-label="Container"
          value={container}
          onChange={(event) => setContainer(event.target.value)}
        >
          {pod.containers.map((entry) => (
            <option key={entry.name} value={entry.name}>
              {entry.name}
            </option>
          ))}
        </Select>
      ) : null}

      {tab === 'overview' ? <Overview pod={pod} /> : null}
      {tab === 'logs' ? (
        <LogView cluster={cluster} pod={pod} container={container} />
      ) : null}
      {tab === 'terminal' ? (
        <Suspense
          fallback={<p className="text-[12px] text-muted">Loading the terminal…</p>}
        >
          <PodTerminal
            clusterId={cluster.id}
            namespace={pod.namespace}
            pod={pod.name}
            container={container}
          />
        </Suspense>
      ) : null}
    </Drawer>
  )
}

function Overview({ pod }: { pod: Pod }) {
  return (
    <>
      <dl className="grid grid-cols-[92px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[12.5px]">
        <dt className="text-muted">Namespace</dt>
        <dd className="truncate font-mono text-fg">{pod.namespace}</dd>
        <dt className="text-muted">Phase</dt>
        <dd className="font-mono text-fg">{pod.phase}</dd>
        <dt className="text-muted">Node</dt>
        <dd className="truncate font-mono text-fg">{pod.node || 'unscheduled'}</dd>
        <dt className="text-muted">Pod IP</dt>
        <dd className="font-mono text-fg">{pod.pod_ip || '—'}</dd>
        <dt className="text-muted">Age</dt>
        <dd className="font-mono text-fg">{relativeAge(pod.created_at)}</dd>
      </dl>

      <div className="flex flex-col gap-1.5">
        <span className="label">Containers</span>
        <ul className="flex flex-col gap-1.5">
          {pod.containers.map((entry) => (
            <li key={entry.name} className="rounded-[5px] border border-line px-2.5 py-2">
              <div className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-fg">
                  {entry.name}
                </span>
                {entry.restarts > 0 ? (
                  <span className="font-mono text-[11.5px] text-warn tabular-nums">
                    {entry.restarts} restarts
                  </span>
                ) : null}
                <Pill tone={entry.ready ? 'ok' : 'warn'}>{entry.state}</Pill>
              </div>
              <p className="mt-1 truncate font-mono text-[11.5px] text-muted" title={entry.image}>
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
function LogView({
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
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex items-center gap-2">
        <Button type="button" onClick={() => setFollowing((current) => !current)}>
          {following ? (
            <Pause aria-hidden="true" className="size-3.5" />
          ) : (
            <Play aria-hidden="true" className="size-3.5" />
          )}
          {following ? 'Stop following' : 'Follow'}
        </Button>
        {!following ? (
          <Button type="button" onClick={() => void load()} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            Reload
          </Button>
        ) : null}
        <span className="ml-auto text-[11.5px] text-muted">
          {following ? 'streaming' : 'last 200 lines'}
        </span>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}

      <pre className="max-h-[420px] min-h-[220px] flex-1 overflow-auto rounded-[5px] border border-ink-line bg-ink px-2.5 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-ink-fg">
        {lines || (loading ? 'Reading…' : 'No output.')}
        <div ref={bottom} />
      </pre>
    </div>
  )
}
