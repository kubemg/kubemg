import { useCallback, useEffect, useMemo, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { errorMessage, fetchPodListMetrics, fetchWorkloadPods } from '../api/client'
import type { Cluster, Pod, PodUsage } from '../api/types'
import type { InsightBucket, InsightSegment } from '../lib/insights'
import { matchesPodBucket, podFailureReason, podInsights } from '../lib/insights'
import { useLiveTick } from '../lib/live'
import type { ResourceKey } from '../lib/resources'
import { TONE_FILL, TONE_SOFT, podTone } from '../lib/status'
import type { Tone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory, podLimit, podUsageIndex, ratio, usageTone } from '../lib/units'
import type { PodUsageIndex } from '../lib/units'
import {
  Button,
  EmptyState,
  Notice,
  OBJECT_MARK,
  OBJECT_NAME,
  Pill,
  Row,
  Table,
  Td,
  Th,
} from './primitives'

/*
 * A workload's health, one pod at a time: what a Deployment/StatefulSet/
 * DaemonSet/Job/CronJob owns right now, whether each one is ready, and what
 * it is actually using against its own limits — the same three questions the
 * pod list itself answers, narrowed to one workload's pods instead of a whole
 * namespace's.
 *
 * It reads in two registers, because "is this thing healthy" and "which pod is
 * the sick one" are two different questions and a table answers only the
 * second:
 *
 *   1. **the state plates** — one tinted tile per non-empty bucket, counted in
 *      the tone the bucket means. This is the part that reads across a room,
 *      and it is what a workload page is opened for: four running, one failed.
 *      Each plate is also the filter for its own bucket, so the answer to "which
 *      one is failed" is a click on the number rather than a scan of the table.
 *   2. **the table** — the rows themselves, with the failing container's own
 *      word (`CrashLoopBackOff`, `ImagePullBackOff`) written under the pod's
 *      name where there is one. A red count with no reason beside it is the
 *      thing that sends somebody to `kubectl describe`.
 *
 * The buckets are `lib/insights.ts`'s, not a second set: the same partition the
 * namespace-wide pod list is summarised by, so the two never disagree about
 * what "Running" counts. The pods are resolved through the same `workload/pods`
 * read the pooled log view uses; a CronJob answers here too, because the backend
 * derives its pods through the Jobs it owns rather than through a selector it
 * does not have.
 *
 * It is deliberately not a second copy of `PodTable` — that table sorts,
 * resizes and offers the manifest column a namespace-wide list needs; this is
 * a small, live-refreshing read with nowhere else to be but here.
 */

export function WorkloadPodsView({
  cluster,
  kind,
  name,
  namespace,
  label,
  onOpenPod,
}: {
  cluster: Cluster
  /** The sidebar's key for the workload, which is how the backend addresses it. */
  kind: ResourceKey
  name: string
  namespace: string
  /** The singular Kind, for the empty and error states. */
  label: string
  /** Opens the clicked pod in this same drawer. */
  onOpenPod: (pod: Pod) => void
}) {
  const [pods, setPods] = useState<Pod[] | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [usage, setUsage] = useState<PodUsageIndex | null>(null)
  const [usageReason, setUsageReason] = useState<string | undefined>(undefined)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  /** Which state plate the table is narrowed to, or null for every pod. */
  const [bucket, setBucket] = useState<InsightBucket | null>(null)

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true)
      try {
        const [resolved, metrics] = await Promise.all([
          fetchWorkloadPods(cluster.id, kind, name, namespace),
          fetchPodListMetrics(cluster.id, namespace).catch(() => null),
        ])
        setPods(resolved.pods)
        setTruncated(resolved.truncated)
        setUsage(metrics?.available ? podUsageIndex(metrics.pods) : null)
        setUsageReason(metrics && !metrics.available ? metrics.reason : undefined)
        setError(null)
      } catch (err) {
        // A quiet re-read that fails leaves the list on screen — the same rule
        // every other live tab in this drawer follows.
        if (!quiet) {
          setError(errorMessage(err, `Could not find this ${label.toLowerCase()}’s pods.`))
          setPods(null)
        }
      } finally {
        if (!quiet) setLoading(false)
      }
    },
    [cluster.id, kind, name, namespace, label],
  )

  useEffect(() => {
    void load()
  }, [load])

  // A different object in the same drawer is a different set of pods, so a
  // narrowing left over from the last one would silently hide rows.
  useEffect(() => setBucket(null), [kind, name, namespace])

  useLiveTick(useCallback(() => load(true), [load]))

  const insight = useMemo(() => podInsights(pods ?? [], usage), [pods, usage])

  if (loading && !pods) {
    return <p className="text-[13px] text-muted">Finding this {label.toLowerCase()}’s pods…</p>
  }
  if (error) return <Notice tone="error">{error}</Notice>
  if (!pods) return null

  if (pods.length === 0) {
    return (
      <EmptyState title="No pods right now">
        This {label.toLowerCase()} owns no pods at the moment. Scale it up, or look at its events to
        find out why.
      </EmptyState>
    )
  }

  const visible = bucket ? pods.filter((pod) => matchesPodBucket(pod, bucket)) : pods
  const narrowed = bucket !== null && visible.length !== pods.length

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {/* The line: what the pods add up to, said in words, with the live
          aggregate on the right where the list header puts it too. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <p className="flex min-w-0 items-center gap-2">
          <span
            aria-hidden="true"
            className={`size-2 shrink-0 rounded-full ${
              insight.headlineTone ? TONE_FILL[insight.headlineTone] : 'bg-faint'
            }`}
          />
          <span className="truncate text-[12.5px] text-muted">{insight.headline}</span>
        </p>
        {insight.summary.length > 0 ? (
          <span className="font-mono text-[11.5px] text-faint tabular-nums">
            {insight.summary.join(' · ')}
          </span>
        ) : null}
        {usageReason ? <span className="text-[11.5px] text-faint">{usageReason}</span> : null}
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="ml-auto"
          onClick={() => void load()}
          disabled={loading}
        >
          <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      </div>

      {/* The plates. Every pod is in exactly one of them and they sum to the
          total, which is what earns them equal width in a row. */}
      <StatePlates
        total={pods.length}
        segments={insight.segments}
        restarting={insight.readings.find((reading) => reading.id === 'restarting')}
        bucket={bucket}
        onBucket={setBucket}
      />

      {truncated ? (
        <Notice tone="info">
          This {label.toLowerCase()} has more pods than kubemg lists at once, so the pods below are
          the first of them.
        </Notice>
      ) : null}

      {narrowed ? (
        <p className="flex items-center gap-2 text-[12px] text-muted">
          Showing {visible.length} of {pods.length} pods.
          <button
            type="button"
            onClick={() => setBucket(null)}
            className="text-accent transition-colors hover:underline"
          >
            Show all
          </button>
        </p>
      ) : null}

      <div className="overflow-x-auto rounded-card border border-line">
        <Table>
          <thead>
            <tr>
              <Th>Pod</Th>
              <Th className="w-[16%]">State</Th>
              <Th className="hidden w-[16%] lg:table-cell">Image</Th>
              <Th className="hidden w-[9%] sm:table-cell">CPU</Th>
              <Th className="hidden w-[9%] sm:table-cell">Memory</Th>
              <Th className="hidden w-[10%] md:table-cell">Restarts</Th>
              <Th className="hidden w-[12%] xl:table-cell">Node</Th>
              <Th className="w-[8%]">Age</Th>
            </tr>
          </thead>
          <tbody>
            {visible.map((pod) => {
              const failure = podFailureReason(pod)
              return (
                <Row key={`${pod.namespace}/${pod.name}`}>
                  <Td>
                    <span className={`flex items-start gap-2.5 ${OBJECT_MARK}`}>
                      <span
                        aria-hidden="true"
                        className={`mt-1.5 size-1.5 shrink-0 rounded-full ${TONE_FILL[podTone(pod)]}`}
                      />
                      <span className="flex min-w-0 flex-col">
                        <button
                          type="button"
                          onClick={() => onOpenPod(pod)}
                          className={`${OBJECT_NAME} text-[12.5px]`}
                          title={pod.name}
                        >
                          {pod.name}
                        </button>
                        {/* The cluster's own word for what is wrong, under the
                            name where the eye already is. Without it a red
                            state is a prompt to go and run `describe`. */}
                        {failure ? (
                          <span className="truncate text-[11.5px] text-danger" title={failure}>
                            {failure}
                          </span>
                        ) : null}
                      </span>
                    </span>
                  </Td>
                  <Td className="whitespace-nowrap">
                    <span className="flex items-center gap-1.5">
                      <Pill tone={podTone(pod)}>{pod.phase}</Pill>
                      <span className="font-mono text-[12px] text-muted">
                        {pod.ready}/{pod.total}
                      </span>
                    </span>
                  </Td>
                  <Td
                    className="hidden truncate font-mono text-[12px] text-muted lg:table-cell"
                    title={podImages(pod).join('\n')}
                  >
                    {podImageLabel(pod)}
                  </Td>
                  <UsageCell
                    usage={usage}
                    pod={pod}
                    resource="cpu"
                    read={(sample) => sample.cpu_millicores}
                    format={formatCPU}
                  />
                  <UsageCell
                    usage={usage}
                    pod={pod}
                    resource="memory"
                    read={(sample) => sample.memory_bytes}
                    format={formatMemory}
                  />
                  <Td
                    className={`hidden font-mono text-[12.5px] md:table-cell ${
                      pod.restarts > 0 ? 'text-warn' : 'text-muted'
                    }`}
                  >
                    {pod.restarts}
                  </Td>
                  <Td className="hidden truncate font-mono text-[12.5px] text-muted xl:table-cell">
                    {pod.node || '—'}
                  </Td>
                  <Td className="whitespace-nowrap font-mono text-[12px] text-muted">
                    {relativeAge(pod.created_at)}
                  </Td>
                </Row>
              )
            })}
          </tbody>
        </Table>
      </div>
    </div>
  )
}

/**
 * The row of state plates, and the only bold mark on this view.
 *
 * A plate is a *bucket of the partition* and nothing else — every pod is in
 * exactly one, and they add up to the total written on the left. The one
 * exception is restarts, which crosses the partition rather than dividing it
 * (a crash-looping pod is Running between restarts), so it sits at the end
 * behind a separator instead of pretending to be a share of anything.
 *
 * Empty buckets are absent rather than zero: on a healthy Deployment this is
 * one plate saying "3 Running", which is the whole answer. Six plates with five
 * zeroes would make that answer harder to read, not easier.
 */
function StatePlates({
  total,
  segments,
  restarting,
  bucket,
  onBucket,
}: {
  total: number
  segments: InsightSegment[]
  restarting?: { value: number; detail?: string }
  bucket: InsightBucket | null
  onBucket: (next: InsightBucket | null) => void
}) {
  return (
    <div className="flex flex-wrap items-stretch gap-2">
      <div className="flex shrink-0 flex-col justify-center pr-1">
        <span className="font-mono text-[19px] leading-none font-semibold text-fg tabular-nums">
          {total}
        </span>
        <span className="label mt-1">{total === 1 ? 'Pod' : 'Pods'}</span>
      </div>
      {segments.map((segment) => (
        <StatePlate
          key={segment.id}
          tone={segment.tone ?? 'idle'}
          value={segment.value}
          label={segment.label}
          active={bucket === segment.id}
          onSelect={() => onBucket(bucket === segment.id ? null : segment.id)}
        />
      ))}
      {restarting && restarting.value > 0 ? (
        <>
          <span aria-hidden="true" className="my-1 w-px shrink-0 bg-line" />
          <StatePlate
            tone="warn"
            value={restarting.value}
            label="Restarts"
            detail={restarting.detail}
            active={bucket === 'restarting'}
            onSelect={() => onBucket(bucket === 'restarting' ? null : 'restarting')}
          />
        </>
      ) : null}
    </div>
  )
}

/**
 * One plate: a count set large, in the tone the state means, on that tone's own
 * tint. Text on a tinted plate is always the plate's own tone — `tone on
 * tone-soft` is the pairing `make frontend-contrast` measures, and `fg` on a
 * tint is a pairing nothing checks.
 *
 * It is a button because the count and the filter are the same thought: the
 * person reading "1 Failed" wants to see that one, and a second control beside
 * the number to do it would be furniture.
 */
function StatePlate({
  tone,
  value,
  label,
  detail,
  active,
  onSelect,
}: {
  tone: Tone
  value: number
  label: string
  detail?: string
  active: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      title={active ? `Showing only ${label.toLowerCase()}` : `Show only ${label.toLowerCase()}`}
      className={`flex min-w-[92px] flex-1 items-center gap-2.5 rounded-card px-3 py-2 text-left transition-shadow ${
        TONE_SOFT[tone]
      } ${active ? 'ring-2 ring-accent-line ring-inset' : 'ring-1 ring-transparent hover:ring-line'}`}
    >
      <span className="font-mono text-[20px] leading-none font-semibold tabular-nums">{value}</span>
      <span className="min-w-0">
        <span className="block truncate text-[12px] font-medium">{label}</span>
        {detail ? <span className="block truncate text-[11px]">{detail}</span> : null}
      </span>
    </button>
  )
}

/** Every image the pod runs, deduplicated and in container order. */
function podImages(pod: Pod): string[] {
  const seen = new Set<string>()
  for (const container of pod.containers) {
    if (container.image) seen.add(container.image)
  }
  return [...seen]
}

/**
 * What the Image column says. A workload's pods almost always run one image and
 * that is the useful case — the tag is how "is the rollout through" is answered
 * without opening anything. A sidecar makes it two, and two truncated paths in a
 * cell is worse than a count, so the column names the first and counts the rest.
 * The full list is on the cell's title either way.
 */
function podImageLabel(pod: Pod): string {
  const images = podImages(pod)
  if (images.length === 0) return '—'
  if (images.length === 1) return images[0]
  return `${images[0]} +${images.length - 1}`
}

/** How a reading against its ceiling reads: comfortable, worth a look, at it. */
const USAGE_TEXT = { ok: 'text-muted', warn: 'text-warn', bad: 'text-danger' } as const

/**
 * UsageCell mirrors the pod list's own reading rather than inventing a second
 * rendering of the same number: a bar needs a denominator most pods do not
 * have, so this is the reading with its percentage beside it where there is
 * one to show.
 */
function UsageCell({
  usage,
  pod,
  resource,
  read,
  format,
}: {
  usage: PodUsageIndex | null
  pod: Pod
  resource: 'cpu' | 'memory'
  read: (sample: PodUsage) => number
  format: (value: number) => string
}) {
  const sample = usage?.get(`${pod.namespace}/${pod.name}`)
  if (!sample) {
    return (
      <Td className="hidden font-mono text-[12.5px] text-faint sm:table-cell">
        <span title={usage ? 'No sample for this pod yet' : 'This cluster serves no Metrics API'}>
          —
        </span>
      </Td>
    )
  }

  const used = read(sample)
  const limit = podLimit(pod.containers, resource)
  const percent = limit > 0 ? ratio(used, limit) : null

  return (
    <Td className="hidden whitespace-nowrap sm:table-cell">
      <span
        className={`font-mono text-[12.5px] ${
          percent === null ? 'text-muted' : USAGE_TEXT[usageTone(percent)]
        }`}
        title={limit > 0 ? `${format(used)} of a ${format(limit)} limit` : `${format(used)}, no limit`}
      >
        {format(used)}
      </span>
      {percent === null ? null : (
        <span className="ml-1.5 font-mono text-[11.5px] text-faint">{Math.round(percent)}%</span>
      )}
    </Td>
  )
}
