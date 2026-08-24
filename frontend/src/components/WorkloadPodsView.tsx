import { useCallback, useEffect, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { errorMessage, fetchPodListMetrics, fetchWorkloadPods } from '../api/client'
import type { Cluster, Pod, PodUsage } from '../api/types'
import { useLiveTick } from '../lib/live'
import type { ResourceKey } from '../lib/resources'
import { TONE_FILL, podTone } from '../lib/status'
import { relativeAge } from '../lib/time'
import { formatCPU, formatMemory, podLimit, podUsageIndex, ratio, usageTone } from '../lib/units'
import type { PodUsageIndex } from '../lib/units'
import { Button, EmptyState, Notice, Pill, Row, Table, Td, Th } from './primitives'

/*
 * A workload's health, one pod at a time: what a Deployment/StatefulSet/
 * DaemonSet/Job/CronJob owns right now, whether each one is ready, and what
 * it is actually using against its own limits — the same three questions the
 * pod list itself answers, narrowed to one workload's pods instead of a whole
 * namespace's.
 *
 * The pods are resolved through the same `workload/pods` read the pooled log
 * view uses; a CronJob answers here too, because the backend derives its pods
 * through the Jobs it owns rather than through a selector it does not have.
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

  useLiveTick(useCallback(() => load(true), [load]))

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

  const running = pods.filter((pod) => podTone(pod) === 'ok').length

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted">
        <span>
          {pods.length} {pods.length === 1 ? 'pod' : 'pods'}, {running} healthy
        </span>
        {usageReason ? <span className="text-faint">· {usageReason}</span> : null}
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

      {truncated ? (
        <Notice tone="info">
          This {label.toLowerCase()} has more pods than kubemg lists at once, so the pods below are
          the first of them.
        </Notice>
      ) : null}

      <div className="overflow-x-auto rounded-card border border-line">
        <Table>
          <thead>
            <tr>
              <Th>Pod</Th>
              <Th className="w-[22%]">Phase</Th>
              <Th className="hidden w-[13%] sm:table-cell">CPU</Th>
              <Th className="hidden w-[13%] sm:table-cell">Memory</Th>
              <Th className="hidden w-[10%] md:table-cell">Restarts</Th>
              <Th className="hidden w-[16%] xl:table-cell">Node</Th>
              <Th className="w-[12%]">Age</Th>
            </tr>
          </thead>
          <tbody>
            {pods.map((pod) => (
              <Row key={`${pod.namespace}/${pod.name}`}>
                <Td className="truncate">
                  <span className="flex items-center gap-2.5">
                    <span
                      aria-hidden="true"
                      className={`size-1.5 shrink-0 rounded-full ${TONE_FILL[podTone(pod)]}`}
                    />
                    <button
                      type="button"
                      onClick={() => onOpenPod(pod)}
                      className="block min-w-0 truncate text-left font-mono text-[12.5px] text-fg transition-colors hover:text-accent"
                      title={pod.name}
                    >
                      {pod.name}
                    </button>
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
            ))}
          </tbody>
        </Table>
      </div>
    </div>
  )
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
