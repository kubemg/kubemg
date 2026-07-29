import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowDownToLine, Pause, Play, RefreshCw, WrapText } from 'lucide-react'
import {
  errorMessage,
  fetchPodLogs,
  fetchWorkloadPods,
  proxyURL,
  readToken,
} from '../api/client'
import type { Cluster, Pod, WorkloadPods } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { Button, Chip, Notice, SearchInput, Select } from './primitives'

/*
 * One workload's log: every pod it owns, interleaved.
 *
 * A Deployment with ten replicas fails on one of them, and reading one pod at a
 * time means opening ten drawers and holding ten tails in your head. So the pods
 * are resolved from the workload's selector and their logs are read together,
 * ordered by the timestamp the cluster put on each line and labelled with which
 * pod said it.
 *
 * It is genuinely N reads, not one: there is no Kubernetes API for a workload's
 * log, and inventing one in the bastion would mean a new streaming shape and a
 * merge nobody could audit. So this opens the same per-pod stream `kubectl logs
 * -f` opens, once per pod — which is also why each one appears in the audit trail
 * on its own, exactly as if it had been opened by hand.
 *
 * That cost is what the cap is for. Eight streams is the ceiling and the number
 * is not arbitrary: it is the deck's categorical palette, and a ninth pod would
 * either repeat a colour or rest its identity on position alone. A workload with
 * more pods than that shows the first eight and the rest are one click away —
 * picking which pods you are reading is the honest form of this control anyway.
 */

/**
 * The eight pod slots, as the class names Tailwind will actually emit. The slot
 * order is the colour-blindness mechanism the palette was validated in, so it is
 * never reordered and never extended.
 */
const POD_STROKE = [
  'text-chart-1',
  'text-chart-2',
  'text-chart-3',
  'text-chart-4',
  'text-chart-5',
  'text-chart-6',
  'text-chart-7',
  'text-chart-8',
] as const

const POD_FILL = [
  'bg-chart-1',
  'bg-chart-2',
  'bg-chart-3',
  'bg-chart-4',
  'bg-chart-5',
  'bg-chart-6',
  'bg-chart-7',
  'bg-chart-8',
] as const

/** How many pods are read at once. One per palette slot; see the note above. */
const MAX_STREAMS = POD_STROKE.length

/** How many lines each pod contributes on a bounded read, and while following. */
const TAIL_PER_POD = 200

/**
 * How many lines the buffer holds across every pod. Eight chatty containers fill
 * this in seconds, and the oldest lines are the ones nobody is looking at.
 */
const MAX_LINES = 4000

/**
 * How many lines are rendered. The buffer is deliberately larger than the view:
 * widening a filter has to be able to reach lines that scrolled away, and it
 * cannot if they were never kept. Each line here is a DOM row rather than text in
 * a `<pre>`, because the pod name has to carry a colour — so the number of rows
 * is a real budget.
 */
const VISIBLE_LINES = 1500

/** One log line, from one pod. */
interface LogEntry {
  pod: string
  container: string
  /** Epoch milliseconds from the line's own timestamp — what the merge orders by. */
  ts: number
  text: string
  /** Insertion order, which keeps the merge stable and the row keyed. */
  seq: number
}

export function WorkloadLogView({
  cluster,
  kind,
  name,
  namespace,
  label,
}: {
  cluster: Cluster
  /** The sidebar's key for the workload, which is how the backend addresses it. */
  kind: ResourceKey
  name: string
  namespace: string
  /** The singular Kind, for the empty and error states. */
  label: string
}) {
  const [resolved, setResolved] = useState<WorkloadPods | null>(null)
  const [resolving, setResolving] = useState(true)
  const [resolveError, setResolveError] = useState<string | null>(null)

  /** Pods the operator has turned off. Everything else is read. */
  const [excluded, setExcluded] = useState<string[]>([])
  const [container, setContainer] = useState('')

  const [entries, setEntries] = useState<LogEntry[]>([])
  const [following, setFollowing] = useState(false)
  const [loading, setLoading] = useState(false)
  /** Per-pod failures. One pod refusing is not the whole view failing. */
  const [failures, setFailures] = useState<string[]>([])
  /** How many streams are still open, for the count next to "streaming". */
  const [live, setLive] = useState(0)

  const [filter, setFilter] = useState('')
  const [wrap, setWrap] = useState(true)
  const [autoScroll, setAutoScroll] = useState(true)
  const bottom = useRef<HTMLDivElement | null>(null)
  const seq = useRef(0)

  const nextSeq = useCallback(() => {
    seq.current += 1
    return seq.current
  }, [])

  const resolve = useCallback(async () => {
    setResolving(true)
    try {
      const result = await fetchWorkloadPods(cluster.id, kind, name, namespace)
      setResolved(result)
      // Keep the chosen container across a refresh where it still exists; a
      // rollout that renamed it falls back to the first one rather than to a
      // name the new pods do not answer for.
      setContainer((current) =>
        current && result.containers.includes(current) ? current : (result.containers[0] ?? ''),
      )
      setResolveError(null)
    } catch (err) {
      setResolveError(errorMessage(err, `Could not find this ${label.toLowerCase()}’s pods.`))
      setResolved(null)
    } finally {
      setResolving(false)
    }
  }, [cluster.id, kind, name, namespace, label])

  useEffect(() => {
    void resolve()
  }, [resolve])

  const pods = resolved?.pods ?? []

  // Memoised so the identity is stable between renders: it is the dependency
  // both the bounded read and the follow effect turn on, and a fresh array every
  // render would reopen every stream on every keystroke in the filter box.
  const targets = useMemo(() => {
    const included = (resolved?.pods ?? []).filter((pod) => !excluded.includes(pod.name))
    return included.slice(0, MAX_STREAMS)
  }, [resolved, excluded])

  const overflow = pods.filter((pod) => !excluded.includes(pod.name)).length - targets.length

  /** Which slot a pod's colour comes from — its place among the pods being read. */
  const slotOf = useCallback(
    (pod: string) => targets.findIndex((entry) => entry.name === pod),
    [targets],
  )

  const load = useCallback(async () => {
    if (targets.length === 0) {
      setEntries([])
      return
    }

    setLoading(true)
    const failed: string[] = []
    try {
      const batches = await Promise.all(
        targets.map(async (pod) => {
          const target = containerFor(pod, container)
          if (target === null) {
            failed.push(`${pod.name} has no container named ${container}`)
            return []
          }
          try {
            const text = await fetchPodLogs(
              cluster.id,
              pod.namespace,
              pod.name,
              target,
              TAIL_PER_POD,
            )
            return parseLines(text.split('\n'), pod.name, target, nextSeq)
          } catch (err) {
            failed.push(`${pod.name}: ${errorMessage(err, 'could not be read')}`)
            return []
          }
        }),
      )
      setEntries(merge([], batches.flat()))
      setFailures(failed)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, targets, container, nextSeq])

  useEffect(() => {
    if (following) return
    void load()
  }, [load, following])

  /*
   * Following opens one stream per pod and appends as each one arrives. A pod
   * that refuses or ends is recorded and the rest keep running — with eight
   * streams open, one of them being a pod that just got rescheduled is normal,
   * and tearing the whole view down for it would be wrong.
   */
  useEffect(() => {
    if (!following) return

    const controller = new AbortController()
    const token = readToken() ?? ''
    const failed: string[] = []
    let ended = 0

    setEntries([])
    setFailures([])
    setLive(targets.length)

    /**
     * One stream is over. A clean end is not a failure — a pod that finished, or
     * one that was rescheduled out from under us, ends its log exactly this way —
     * so only a reason worth reading becomes a notice. Either way, when the last
     * stream is gone nothing is being followed and the button has to say so.
     */
    function stop(reason?: string) {
      if (reason) {
        failed.push(reason)
        setFailures([...failed])
      }
      ended += 1
      setLive(targets.length - ended)
      if (ended === targets.length) setFollowing(false)
    }

    async function follow(pod: Pod) {
      const target = containerFor(pod, container)
      if (target === null) {
        stop(`${pod.name} has no container named ${container}`)
        return
      }

      const query = new URLSearchParams({
        follow: 'true',
        timestamps: 'true',
        tailLines: String(TAIL_PER_POD),
      })
      if (target) query.set('container', target)

      try {
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
        // A chunk boundary lands mid-line often enough to matter: a half line
        // parsed now would lose its timestamp and sort to the wrong place.
        let partial = ''
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          partial += decoder.decode(value, { stream: true })
          const lines = partial.split('\n')
          partial = lines.pop() ?? ''
          if (lines.length === 0) continue

          const parsed = parseLines(lines, pod.name, target, nextSeq)
          setEntries((current) => merge(current, parsed))
        }
        stop()
      } catch (err) {
        if (controller.signal.aborted) return
        stop(`${pod.name}: ${err instanceof Error ? err.message : 'the stream stopped'}`)
      }
    }

    targets.forEach((pod) => void follow(pod))
    return () => controller.abort()
  }, [following, cluster.id, targets, container, nextSeq])

  // Filtering is a view over the buffer, never a filter on the streams: the point
  // of narrowing a live log is being able to widen it again without having lost
  // the lines you were not looking at. The pod name is searchable too, which is
  // how a pooled view is narrowed back down to one pod without stopping it.
  const { visible, total, matched } = useMemo(() => {
    const needle = filter.trim().toLowerCase()
    const hits =
      needle === ''
        ? entries
        : entries.filter(
            (entry) =>
              entry.text.toLowerCase().includes(needle) ||
              entry.pod.toLowerCase().includes(needle),
          )
    return { visible: hits.slice(-VISIBLE_LINES), total: entries.length, matched: hits.length }
  }, [entries, filter])

  useEffect(() => {
    if (following && autoScroll) bottom.current?.scrollIntoView({ block: 'end' })
  }, [visible, following, autoScroll])

  if (resolveError) return <Notice tone="error">{resolveError}</Notice>
  if (resolving && !resolved) {
    return <p className="text-[13px] text-muted">Finding this {label.toLowerCase()}’s pods…</p>
  }
  if (pods.length === 0) {
    return (
      <Notice tone="info">
        This {label.toLowerCase()} has no pods right now, so there is nothing to read. Scale it up,
        or look at its events to find out why the pods are not there.
      </Notice>
    )
  }

  const empty =
    filter.trim() !== ''
      ? 'No line matches that.'
      : loading
        ? 'Reading…'
        : following
          ? 'Waiting for output…'
          : 'No output.'

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          size="sm"
          onClick={() => setFollowing((current) => !current)}
          disabled={targets.length === 0}
        >
          {following ? (
            <Pause aria-hidden="true" className="size-3.5" />
          ) : (
            <Play aria-hidden="true" className="size-3.5" />
          )}
          {following ? 'Stop following' : `Follow ${targets.length} pods`}
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
          placeholder="Filter lines or pods"
          label="Filter log lines"
        />

        {/* One container across every pod: they come from one pod template, so
            naming a container names the same process on all of them. */}
        {(resolved?.containers.length ?? 0) > 1 ? (
          <div className="w-40">
            <Select
              aria-label="Container"
              size="sm"
              value={container}
              onChange={(event) => setContainer(event.target.value)}
            >
              {resolved?.containers.map((entry) => (
                <option key={entry} value={entry}>
                  {entry}
                </option>
              ))}
            </Select>
          </div>
        ) : null}

        <Chip active={wrap} onClick={() => setWrap((current) => !current)}>
          <WrapText aria-hidden="true" className="size-3.5" />
          Wrap
        </Chip>
        <Chip active={autoScroll} onClick={() => setAutoScroll((current) => !current)}>
          <ArrowDownToLine aria-hidden="true" className="size-3.5" />
          Tail
        </Chip>
      </div>

      {/* Which pods are being read, and their colours. It is a row of toggles
          rather than a legend because the two things an operator does with a
          pooled log are the same act: seeing which pod a line came from, and
          taking the noisy one out. */}
      <div className="flex flex-wrap items-center gap-1.5">
        {pods.map((pod) => {
          const slot = slotOf(pod.name)
          const reading = slot >= 0
          return (
            <button
              key={pod.name}
              type="button"
              aria-pressed={reading}
              onClick={() =>
                setExcluded((current) =>
                  current.includes(pod.name)
                    ? current.filter((entry) => entry !== pod.name)
                    : [...current, pod.name],
                )
              }
              title={reading ? `Stop reading ${pod.name}` : `Read ${pod.name}`}
              className={`inline-flex h-7 max-w-56 min-w-0 items-center gap-1.5 rounded-chip border px-2 font-mono text-[11.5px] transition-colors ${
                reading
                  ? 'border-line bg-raised text-fg'
                  : 'border-line-soft bg-surface text-faint hover:text-muted'
              }`}
            >
              <span
                aria-hidden="true"
                className={`size-1.5 shrink-0 rounded-full ${
                  reading ? POD_FILL[slot % POD_FILL.length] : 'bg-line'
                }`}
              />
              <span className="truncate">{shortName(pod.name, name)}</span>
            </button>
          )
        })}
      </div>

      {overflow > 0 ? (
        <Notice tone="info">
          {`${overflow} more ${overflow === 1 ? 'pod is' : 'pods are'} not being read: eight at a time is the limit, since each one is a stream of its own. Turn one off to make room.`}
        </Notice>
      ) : null}

      {resolved?.truncated ? (
        <Notice tone="info">
          This {label.toLowerCase()} has more pods than KubeMG lists at once, so the pods above are
          the first of them.
        </Notice>
      ) : null}

      <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted">
        {following ? (
          <span className="flex items-center gap-2">
            <span aria-hidden="true" className="breathe size-1.5 rounded-full bg-ok" />
            streaming {live} of {targets.length} pods
          </span>
        ) : (
          <span>
            last {TAIL_PER_POD} lines per pod, from {targets.length}{' '}
            {targets.length === 1 ? 'pod' : 'pods'}
          </span>
        )}
        <span className="ml-auto font-mono text-[11.5px] text-faint tabular-nums">
          {filter.trim() !== '' ? `${matched} of ${total} lines` : `${total} lines`}
          {matched > VISIBLE_LINES ? ` · showing the last ${VISIBLE_LINES}` : ''}
        </span>
      </div>

      {failures.length > 0 ? (
        <Notice tone="warn">
          {failures.length === 1
            ? failures[0]
            : `${failures.length} pods stopped: ${failures.join('; ')}`}
        </Notice>
      ) : null}

      <div className="max-h-[420px] min-h-[240px] flex-1 overflow-auto rounded-card border border-line bg-sunken px-3 py-2.5 font-mono text-[12px] leading-relaxed text-fg">
        {visible.length === 0 ? <span className="text-muted">{empty}</span> : null}
        {visible.map((entry) => {
          const slot = slotOf(entry.pod)
          return (
            <div key={entry.seq} className="flex gap-2">
              <span className="shrink-0 text-[11.5px] text-faint tabular-nums">
                {clockTime(entry.ts)}
              </span>
              <span
                title={entry.pod}
                className={`w-28 shrink-0 truncate text-[11.5px] ${
                  slot >= 0 ? POD_STROKE[slot % POD_STROKE.length] : 'text-muted'
                }`}
              >
                {shortName(entry.pod, name)}
              </span>
              <span className={`min-w-0 flex-1 ${wrap ? 'break-words whitespace-pre-wrap' : 'whitespace-pre'}`}>
                {entry.text}
              </span>
            </div>
          )
        })}
        <div ref={bottom} />
      </div>
    </div>
  )
}

/**
 * containerFor picks the container to read on one pod. An empty choice means the
 * cluster picks, which it can do only where there is one container. `null` means
 * this pod does not have the container being read — which happens mid-rollout,
 * across a template that renamed or added one.
 */
function containerFor(pod: Pod, container: string): string | null {
  if (!container) return ''
  if (pod.containers.some((entry) => entry.name === container)) return container
  if (pod.containers.length === 1) return ''
  return null
}

/**
 * parseLines splits the timestamp Kubernetes prefixed each line with off the line
 * itself. The timestamp is the whole reason the pooled view can be ordered at all
 * — without it, interleaving eight streams would only show the order the chunks
 * happened to arrive in, which is the order of the network and not of the logs.
 *
 * A line with no readable timestamp is kept and stamped now: dropping it would
 * lose output, and the only thing lost by stamping it is its place in the order.
 */
function parseLines(
  lines: string[],
  pod: string,
  container: string,
  nextSeq: () => number,
): LogEntry[] {
  const out: LogEntry[] = []
  for (const line of lines) {
    if (line === '') continue
    const space = line.indexOf(' ')
    let ts = Number.NaN
    let text = line
    if (space > 0) {
      const parsed = Date.parse(line.slice(0, space))
      if (!Number.isNaN(parsed)) {
        ts = parsed
        text = line.slice(space + 1)
      }
    }
    out.push({
      pod,
      container,
      ts: Number.isNaN(ts) ? Date.now() : ts,
      text,
      seq: nextSeq(),
    })
  }
  return out
}

/**
 * merge folds a batch into the buffer in timestamp order. Each stream arrives
 * ordered on its own, and in the common case a batch is newer than everything
 * held — so the ordinary path is an append and the sort is what a pod that
 * reconnected, or one whose clock is behind, falls back to.
 */
function merge(current: LogEntry[], incoming: LogEntry[]): LogEntry[] {
  if (incoming.length === 0) return current

  const newest = current.length > 0 ? current[current.length - 1].ts : Number.NEGATIVE_INFINITY
  const next = current.concat(incoming)
  if (incoming[0].ts < newest) {
    // Stable on seq, so two lines written in the same millisecond keep the order
    // they arrived in rather than swapping on every merge.
    next.sort((a, b) => a.ts - b.ts || a.seq - b.seq)
  }
  return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
}

/**
 * shortName drops the workload's own name from its pods'. Every pod in a
 * Deployment starts with the same fifteen characters, and a column of them is a
 * column of the one thing that does not distinguish the rows.
 */
function shortName(pod: string, workload: string): string {
  const trimmed = pod.startsWith(`${workload}-`) ? pod.slice(workload.length + 1) : pod
  return trimmed === '' ? pod : trimmed
}

/** The wall-clock time of a line. The date is the same for all of them. */
function clockTime(ts: number): string {
  const at = new Date(ts)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`
}
