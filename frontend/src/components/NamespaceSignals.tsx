/*
 * The namespace block of the Explore pilot header: what this namespace spends,
 * on which workload, and which of its pods are in trouble for a reason the
 * phase bar cannot show.
 *
 * One row of five cells under the bar, the composition first because it is the
 * widest and the only drawing; then four columns that each answer one question
 * with a count and the pods behind it:
 *
 *   - **Restarts** — from the pod rows.
 *   - **Image pull** — from the pod rows, in the kubelet's own words.
 *   - **Throttled** — from the cluster's datasource, the only cell that reads
 *     history, over the header's own time range. No datasource is a state the
 *     cell names, not an error, the same way the trend region does.
 *   - **At limit** — a previous OOM kill from the pod rows, or a live sample
 *     near a container's own limit from metrics-server.
 *
 * Every number is derived in `lib/namespaceSignals.ts`; this file only draws.
 * Nothing here moves: the donut answers a hover by dimming the other slices,
 * never by animating, and a hover is the only thing that changes it.
 *
 * It appears only where exactly one namespace is selected, for the same reason
 * the trend region does: these are namespace readings, and under "All
 * namespaces" there is no honest equivalent for the composition.
 */

import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { ArrowUpToLine, ChartPie, ChevronDown, Gauge, ImageOff, LineChart, RotateCcw } from 'lucide-react'
import {
  compareMetrics,
  fetchPodListMetrics,
  fetchPods,
  queryError,
  unconfigured,
} from '../api/client'
import type { Cluster, Pod } from '../api/types'
import {
  NAMED_SIGNALS,
  THROTTLE_PRESSURE,
  consumption,
  imagePullSignal,
  limitSignal,
  restartSignal,
  throttleSignal,
} from '../lib/namespaceSignals'
import type { ConsumptionAxis, ConsumptionSlice, Signal } from '../lib/namespaceSignals'
import { queryKey, useCachedQuery } from '../lib/query'
import { TONE_TEXT } from '../lib/status'
import { queryRangeLabel } from '../lib/timerange'
import { formatCPU, formatMemory, podUsageIndex } from '../lib/units'
import type { PodUsageIndex } from '../lib/units'
import { useTimeRange } from '../state/timerange-context'
import { Button, Segmented } from './primitives'

/**
 * The eight composition slots as literal class names — Tailwind reads the source
 * for them, so an interpolated `text-chart-${n}` would compile to nothing. The
 * order is the colour-blindness mechanism (index.css): never reorder, never add
 * a ninth. A slice never rests on its colour alone; the legend beside it names
 * every one.
 */
const SLICE_STROKE = [
  'text-chart-1',
  'text-chart-2',
  'text-chart-3',
  'text-chart-4',
  'text-chart-5',
  'text-chart-6',
  'text-chart-7',
  'text-chart-8',
] as const

const SLICE_FILL = [
  'bg-chart-1',
  'bg-chart-2',
  'bg-chart-3',
  'bg-chart-4',
  'bg-chart-5',
  'bg-chart-6',
  'bg-chart-7',
  'bg-chart-8',
] as const

/** The pods behind the block, either handed over or read here. */
interface Source {
  pods: Pod[]
  usage: PodUsageIndex | null
  usageReason?: string
}

export function NamespaceSignals({
  cluster,
  namespace,
  loaded,
  onOpenPod,
  onRestarting,
  restartingActive = false,
  history,
}: {
  cluster: Cluster
  namespace: string
  /**
   * The namespace's pods and their live sample, when the list on screen *is*
   * the pod list — it already holds both, so reading them again would be a
   * second call for the same answer. Absent over a Deployment or Job list, and
   * then the block reads the pods itself, through the same cache.
   */
  loaded?: Source
  onOpenPod: (pod: Pod) => void
  /** Narrows the list to restarting pods. Only the pod list has that matcher. */
  onRestarting?: () => void
  restartingActive?: boolean
  /**
   * The namespace's usage history — the trend region under this block. It is
   * not drawn until asked for: a curve over the whole range is the slowest
   * read on the page and the least often needed one, so the composition
   * carries the door to it rather than the band carrying the chart.
   */
  history?: { open: boolean; onToggle: () => void }
}) {
  const { range } = useTimeRange()

  const own = useCachedQuery<Source>(
    loaded ? null : queryKey('namespace-signals', cluster.id, namespace),
    async () => {
      const [pods, metrics] = await Promise.all([
        fetchPods(cluster.id, namespace),
        fetchPodListMetrics(cluster.id, namespace).catch(() => null),
      ])
      return {
        pods,
        usage: metrics?.available ? podUsageIndex(metrics.pods) : null,
        usageReason: metrics && !metrics.available ? metrics.reason : undefined,
      }
    },
    { live: true },
  )

  const throttling = useCachedQuery(
    queryKey('metrics-compare', cluster.id, 'cpu_throttling', namespace, range),
    () => compareMetrics(cluster.id, 'cpu_throttling', { namespace, topk: 20, range }),
  )

  const source = loaded ?? own.data
  const pods = useMemo(() => source?.pods ?? [], [source])
  const byName = useMemo(() => new Map(pods.map((pod) => [pod.name, pod])), [pods])

  const restarts = useMemo(() => restartSignal(pods), [pods])
  const images = useMemo(() => imagePullSignal(pods), [pods])
  const limits = useMemo(() => limitSignal(pods, source?.usage ?? null), [pods, source])
  const throttled = useMemo(
    () => (throttling.data ? throttleSignal(throttling.data.result.rows, namespace) : null),
    [throttling.data, namespace],
  )

  if (!source) {
    return (
      <p role="status" className="px-5 py-6 text-center text-[12.5px] text-muted">
        {own.error ? 'Could not read this namespace’s pods.' : 'Reading this namespace’s pods…'}
      </p>
    )
  }

  const open = (name: string) => {
    const pod = byName.get(name)
    return pod ? () => onOpenPod(pod) : undefined
  }

  const throttleMissing = unconfigured(throttling.error)

  return (
    // The composition on the left, the four signals as a 2×2 beside it. Four
    // equal columns at this width left a pod name about twenty characters, and
    // a pod name is the thing a signal exists to hand over. The 1px gaps over
    // the line colour are the dividers.
    <div className="grid grid-cols-1 gap-px bg-line-soft sm:grid-cols-2 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)_minmax(0,1fr)]">
      <div className="bg-surface sm:col-span-2 lg:col-span-1 lg:row-span-2">
        <Composition
          pods={pods}
          usage={source.usage}
          reason={source.usageReason}
          history={history}
          range={queryRangeLabel(range)}
        />
      </div>

      <SignalCell
        icon={<RotateCcw aria-hidden="true" className="size-3.5" />}
        title="Restarts"
        signal={restarts}
        lead={restarts.restarts}
        caption={
          restarts.count > 0
            ? `across ${restarts.count} of ${pods.length} pod${pods.length === 1 ? '' : 's'}`
            : 'No container has restarted'
        }
        open={open}
        onLead={onRestarting}
        leadActive={restartingActive}
        leadTitle="Show only restarting pods"
      />

      <SignalCell
        icon={<ImageOff aria-hidden="true" className="size-3.5" />}
        title="Image pull"
        signal={images}
        lead={images.count}
        caption={images.count > 0 ? 'pods waiting on an image' : 'Every image arrived'}
        open={open}
      />

      <SignalCell
        icon={<Gauge aria-hidden="true" className="size-3.5" />}
        title="Throttled"
        signal={throttled}
        lead={throttled?.count ?? null}
        caption={
          throttleMissing
            ? 'No datasource — throttling is read from history'
            : throttling.error
              ? queryError(throttling.error, 'Could not read throttling.')
              : !throttled
                ? 'Reading…'
                : throttled.count > 0
                  ? `pods past ${Math.round(THROTTLE_PRESSURE * 100)}% of CPU periods · ${queryRangeLabel(range)}`
                  : `None past ${Math.round(THROTTLE_PRESSURE * 100)}% of CPU periods · ${queryRangeLabel(range)}`
        }
        open={open}
      />

      <SignalCell
        icon={<ArrowUpToLine aria-hidden="true" className="size-3.5" />}
        title="At limit"
        signal={limits}
        lead={limits.limited > 0 || limits.count > 0 ? limits.count : null}
        caption={
          limits.limited === 0 && limits.count === 0
            ? 'No pod here declares a limit'
            : limits.count > 0
              ? `of ${limits.limited} pod${limits.limited === 1 ? '' : 's'} with limits${limits.measured ? '' : ' · OOM kills only'}`
              : `None near a limit${limits.measured ? '' : ' — no live sample, OOM kills only'}`
        }
        open={open}
      />
    </div>
  )
}

/**
 * The composition: a donut of the namespace's live usage per workload, and a
 * legend that names every slice with its figure and share.
 *
 * Drawn from the live sample rather than the datasource, so it is there on a
 * cluster that registered no metrics backend — a composition of one moment is a
 * share, not a series, so it does not need history to be honest.
 */
function Composition({
  pods,
  usage,
  reason,
  history,
  range,
}: {
  pods: Pod[]
  usage: PodUsageIndex | null
  reason?: string
  history?: { open: boolean; onToggle: () => void }
  range: string
}) {
  const [axis, setAxis] = useState<ConsumptionAxis>('cpu')
  const [hover, setHover] = useState<string | null>(null)
  const reading = useMemo(() => consumption(pods, usage, axis), [pods, usage, axis])
  const format = axis === 'cpu' ? formatCPU : formatMemory
  const focused = reading?.slices.find((slice) => slice.key === hover) ?? null

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 px-5 py-4">
      <div className="flex items-center gap-2">
        <ChartPie aria-hidden="true" className="size-3.5 text-faint" />
        <p className="truncate text-[13px] font-semibold text-fg">Consumption by workload</p>
        <span className="shrink-0 text-[11.5px] text-faint">live</span>
        <div className="ml-auto">
          <Segmented<ConsumptionAxis>
            ariaLabel="Which reading to divide"
            value={axis}
            onChange={setAxis}
            options={[
              { value: 'cpu', label: 'CPU' },
              { value: 'memory', label: 'Memory' },
            ]}
          />
        </div>
      </div>

      {!reading ? (
        <div className="flex flex-1 items-center gap-4">
          <Ring />
          <p className="min-w-0 text-[12px] text-muted">
            No live sample for this namespace.
            {reason ? <span className="mt-1 block text-[11.5px] text-faint">{reason}</span> : null}
          </p>
        </div>
      ) : reading.slices.length === 0 ? (
        <div className="flex flex-1 items-center gap-4">
          <Ring />
          <p className="text-[12px] text-muted">Nothing here is spending {axis === 'cpu' ? 'CPU' : 'memory'}.</p>
        </div>
      ) : (
        <div className="flex flex-1 flex-wrap items-center gap-x-6 gap-y-3">
          <Donut
            slices={reading.slices}
            hover={hover}
            onHover={setHover}
            center={focused ? format(focused.value) : format(reading.total)}
            caption={
              focused
                ? `${Math.round(focused.share * 100)}%`
                : axis === 'cpu'
                  ? 'CPU'
                  : 'Memory'
            }
          />
          <ul className="flex min-w-0 flex-1 basis-60 flex-col gap-0.5">
            {reading.slices.map((slice) => (
              <LegendRow
                key={slice.key}
                slice={slice}
                value={format(slice.value)}
                dimmed={hover !== null && hover !== slice.key}
                onHover={setHover}
              />
            ))}
            {reading.sampled < reading.pods ? (
              <li className="pt-1 text-[11px] text-faint">
                Sampled {reading.sampled} of {reading.pods} pods
              </li>
            ) : null}
          </ul>
        </div>
      )}

      {history ? (
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 border-t border-line-soft pt-3">
          <Button
            type="button"
            size="sm"
            variant={history.open ? 'secondary' : 'primary'}
            onClick={history.onToggle}
            aria-expanded={history.open}
          >
            <LineChart aria-hidden="true" className="size-3.5" />
            {history.open ? 'Hide usage history' : 'Show usage history'}
            <ChevronDown
              aria-hidden="true"
              className={`size-3.5 transition-transform duration-300 motion-reduce:transition-none ${
                history.open ? 'rotate-180' : ''
              }`}
            />
          </Button>
          <span className="text-[11.5px] text-faint">{range} · CPU and memory per pod</span>
        </div>
      ) : null}
    </div>
  )
}

/** Donut geometry, in SVG user units. */
const SIZE = 112
const STROKE = 14
const RADIUS = (SIZE - STROKE) / 2
const CIRCUMFERENCE = 2 * Math.PI * RADIUS
/** The surface gap between two slices, so neighbours never touch. */
const GAP = 2

function Donut({
  slices,
  hover,
  onHover,
  center,
  caption,
}: {
  slices: ConsumptionSlice[]
  hover: string | null
  onHover: (key: string | null) => void
  center: string
  caption: string
}) {
  const gap = slices.length > 1 ? GAP : 0
  let offset = 0

  return (
    // Decoration to a screen reader: the legend beside it is the readable
    // version of exactly the same numbers.
    <div className="relative shrink-0" style={{ width: SIZE, height: SIZE }}>
      <svg
        aria-hidden="true"
        viewBox={`0 0 ${SIZE} ${SIZE}`}
        className="-rotate-90"
        width={SIZE}
        height={SIZE}
        onMouseLeave={() => onHover(null)}
      >
        <circle
          cx={SIZE / 2}
          cy={SIZE / 2}
          r={RADIUS}
          fill="none"
          strokeWidth={STROKE}
          className="stroke-raised"
        />
        {slices.map((slice) => {
          const length = slice.share * CIRCUMFERENCE
          // A sliver keeps a visible floor so a workload spending a fraction
          // of a percent is still a mark, not a hairline under the gap.
          const drawn = Math.max(length - gap, Math.min(length, 1.5))
          const dash = `${drawn} ${CIRCUMFERENCE - drawn}`
          const at = -offset
          offset += length
          return (
            <circle
              key={slice.key}
              cx={SIZE / 2}
              cy={SIZE / 2}
              r={RADIUS}
              fill="none"
              stroke="currentColor"
              strokeWidth={STROKE}
              strokeDasharray={dash}
              strokeDashoffset={at}
              onMouseEnter={() => onHover(slice.key)}
              className={`${SLICE_STROKE[slice.slot]} transition-opacity duration-300 motion-reduce:transition-none ${
                hover !== null && hover !== slice.key ? 'opacity-30' : ''
              }`}
            />
          )
        })}
      </svg>
      <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
        <span className="font-data text-[16px] leading-tight font-bold text-fg tabular-nums">
          {center}
        </span>
        <span className="text-[11px] text-faint">{caption}</span>
      </div>
    </div>
  )
}

/** The empty ring, for a namespace with nothing to divide. */
function Ring() {
  return (
    <svg aria-hidden="true" viewBox={`0 0 ${SIZE} ${SIZE}`} width={SIZE / 1.4} height={SIZE / 1.4} className="shrink-0">
      <circle
        cx={SIZE / 2}
        cy={SIZE / 2}
        r={RADIUS}
        fill="none"
        strokeWidth={STROKE}
        strokeDasharray="4 4"
        className="stroke-line"
      />
    </svg>
  )
}

function LegendRow({
  slice,
  value,
  dimmed,
  onHover,
}: {
  slice: ConsumptionSlice
  value: string
  dimmed: boolean
  onHover: (key: string | null) => void
}) {
  const title = slice.kind
    ? `${slice.kind} ${slice.label} — ${slice.pods} pod${slice.pods === 1 ? '' : 's'}`
    : `${slice.label} workloads — ${slice.pods} pods`
  return (
    <li
      title={title}
      onMouseEnter={() => onHover(slice.key)}
      onMouseLeave={() => onHover(null)}
      className={`flex min-w-0 items-center gap-2 rounded-chip px-1.5 py-0.5 transition-opacity duration-300 motion-reduce:transition-none ${
        dimmed ? 'opacity-40' : ''
      }`}
    >
      <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${SLICE_FILL[slice.slot]}`} />
      <span className="min-w-0 flex-1 truncate font-data text-[12px] text-fg">{slice.label}</span>
      <span className="shrink-0 font-data text-[12px] text-muted tabular-nums">{value}</span>
      <span className="w-9 shrink-0 text-right font-data text-[11.5px] text-faint tabular-nums">
        {Math.round(slice.share * 100)}%
      </span>
    </li>
  )
}

/**
 * One question, one count, and the pods behind it. The count is null where the
 * question cannot be answered here at all — no datasource, nothing limited —
 * and then the caption says why instead of a zero claiming "none".
 */
function SignalCell({
  icon,
  title,
  signal,
  lead,
  caption,
  open,
  onLead,
  leadActive = false,
  leadTitle,
}: {
  icon: ReactNode
  title: string
  signal: Signal | null
  lead: number | null
  caption: string
  open: (name: string) => (() => void) | undefined
  onLead?: () => void
  leadActive?: boolean
  leadTitle?: string
}) {
  const named = signal?.pods.slice(0, NAMED_SIGNALS) ?? []
  const rest = (signal?.count ?? 0) - named.length
  const worst = signal?.pods[0]?.tone
  const tint = lead !== null && lead > 0 && worst ? TONE_TEXT[worst] : 'text-fg'

  const figure = (
    <span className={`font-data text-[22px] leading-none font-bold tabular-nums ${lead === null ? 'text-faint' : tint}`}>
      {lead === null ? '—' : lead}
    </span>
  )

  return (
    <div className="flex min-w-0 flex-col gap-2 bg-surface px-5 py-4">
      <p className="flex items-center gap-1.5 text-faint">
        {icon}
        <span className="label">{title}</span>
      </p>

      <div className="flex flex-col gap-1">
        {onLead && lead !== null && lead > 0 ? (
          <button
            type="button"
            onClick={onLead}
            aria-pressed={leadActive}
            title={leadTitle}
            className={`-mx-1.5 self-start rounded-control px-1.5 py-0.5 transition-colors ${
              leadActive ? 'bg-accent-soft ring-1 ring-accent-line ring-inset' : 'hover:bg-raised'
            }`}
          >
            {figure}
          </button>
        ) : (
          figure
        )}
        <span className="text-[11.5px] leading-snug text-muted">{caption}</span>
      </div>

      {named.length > 0 ? (
        <ul className="flex flex-col gap-0.5">
          {named.map((pod) => {
            const onClick = open(pod.name)
            const body = (
              <>
                <span className="min-w-0 flex-1 truncate text-left font-data text-[12px] text-fg">
                  {pod.name}
                </span>
                <span className={`shrink-0 font-data text-[11.5px] tabular-nums ${TONE_TEXT[pod.tone]}`}>
                  {pod.detail}
                </span>
              </>
            )
            return (
              <li key={pod.key} className="min-w-0">
                {onClick ? (
                  <button
                    type="button"
                    onClick={onClick}
                    title={`${pod.namespace}/${pod.name} — ${pod.detail}`}
                    className="-mx-1.5 flex w-[calc(100%+0.75rem)] min-w-0 items-baseline gap-2 rounded-chip px-1.5 py-0.5 transition-colors duration-300 hover:bg-raised"
                  >
                    {body}
                  </button>
                ) : (
                  // A throttled pod from earlier in the window may have gone
                  // since; it is still named, but there is no row to open.
                  <span
                    title={`${pod.namespace}/${pod.name} — no longer running`}
                    className="flex min-w-0 items-baseline gap-2 py-0.5"
                  >
                    {body}
                  </span>
                )}
              </li>
            )
          })}
          {rest > 0 ? <li className="pt-0.5 text-[11.5px] text-faint">and {rest} more</li> : null}
        </ul>
      ) : null}
    </div>
  )
}
