import type { ReactNode } from 'react'
import type { CapacityDimension, PodSlots } from '../api/types'
import type { Segment } from '../lib/capacityView'
import { allocationTone, clampPercent, percentText, segmentShares } from '../lib/capacityView'

/*
 * The capacity page's pictures. Every one is a reading the server already
 * made, drawn rather than written: reserved is the fill, live use is a mark on
 * the same track, and the sentence that used to say both is the tooltip.
 *
 * Limits are still never drawn — they routinely exceed the node, and a ring or
 * bar that ran off its own end would misreport by exactly that much. They are
 * a figure beside the picture.
 */

const STROKE = { ok: 'stroke-ok', warn: 'stroke-warn', bad: 'stroke-danger' } as const
const FILL = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-danger' } as const
const TEXT = { ok: 'text-fg', warn: 'text-warn', bad: 'text-danger' } as const

/* Ring geometry, in SVG user units: a 270° sweep with the gap at the bottom,
   the same shape as the dashboard's dial so the two read as one vocabulary. */
const SIZE = 132
const RADIUS = 52
const WIDTH = 11
const SWEEP = 270
const START = 135 // degrees clockwise from three o'clock: bottom-left
const CIRCUMFERENCE = 2 * Math.PI * RADIUS
const ARC = (SWEEP / 360) * CIRCUMFERENCE

function ringPoint(percent: number, radius: number): [number, number] {
  const radians = ((START + (SWEEP * percent) / 100) * Math.PI) / 180
  return [SIZE / 2 + radius * Math.cos(radians), SIZE / 2 + radius * Math.sin(radians)]
}

/**
 * CapacityRing is one cluster-wide ceiling as a ring: filled to what is
 * reserved, a bright mark where live use sits, the warning line as a dot on
 * the rim. The figures under it are the only words.
 */
export function CapacityRing({
  label,
  reservedPercent,
  usedPercent,
  measured,
  amount,
  footer,
  title,
}: {
  label: string
  reservedPercent: number
  /** Where live use sits, or null where it is not measured. */
  usedPercent: number | null
  /** False when the node reports no allocatable — the ring then draws empty. */
  measured: boolean
  /** `1.33 / 8.00 cores`, already formatted. */
  amount: string
  /** Small figures under the amount — in use, limits. */
  footer?: ReactNode
  /** The full reading, for the pointer and a screen reader. */
  title: string
}) {
  const tone = allocationTone(reservedPercent)
  const filled = measured ? (clampPercent(reservedPercent) / 100) * ARC : 0
  const [warnX, warnY] = ringPoint(90, RADIUS + WIDTH / 2 + 5)

  return (
    <div className="flex min-w-0 flex-col items-center" title={title}>
      <div
        role="meter"
        aria-label={`${label} reserved`}
        aria-valuenow={measured ? Math.round(reservedPercent) : undefined}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={title}
        className="relative w-full max-w-[176px]"
      >
        <svg aria-hidden="true" viewBox={`0 0 ${SIZE} ${SIZE}`} className="block w-full">
          <g transform={`rotate(${START} ${SIZE / 2} ${SIZE / 2})`}>
            <circle
              cx={SIZE / 2}
              cy={SIZE / 2}
              r={RADIUS}
              fill="none"
              strokeWidth={WIDTH}
              strokeLinecap="round"
              strokeDasharray={`${ARC} ${CIRCUMFERENCE}`}
              className="stroke-raised"
            />
            {filled > 0 ? (
              <circle
                cx={SIZE / 2}
                cy={SIZE / 2}
                r={RADIUS}
                fill="none"
                strokeWidth={WIDTH}
                strokeLinecap="round"
                strokeDasharray={`${filled} ${CIRCUMFERENCE}`}
                className={STROKE[tone]}
              />
            ) : null}
          </g>
          {measured ? <circle cx={warnX} cy={warnY} r={2.25} className="fill-warn" /> : null}
          {usedPercent !== null && measured
            ? (() => {
                const [x1, y1] = ringPoint(clampPercent(usedPercent), RADIUS - WIDTH / 2 - 3)
                const [x2, y2] = ringPoint(clampPercent(usedPercent), RADIUS + WIDTH / 2 + 3)
                return (
                  <line
                    x1={x1}
                    y1={y1}
                    x2={x2}
                    y2={y2}
                    strokeWidth={2.5}
                    strokeLinecap="round"
                    className="stroke-fg"
                  />
                )
              })()
            : null}
        </svg>
        <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
          <span
            className={`font-data text-[26px] leading-none font-bold tabular-nums ${TEXT[tone]}`}
          >
            {measured ? percentText(reservedPercent) : '—'}
          </span>
          <span className="mt-1 text-[12px] text-muted">{label}</span>
        </div>
      </div>
      <p className="-mt-2 font-data text-[12.5px] text-fg tabular-nums">{amount}</p>
      {footer ? (
        <div className="mt-1 flex flex-wrap justify-center gap-x-2.5 gap-y-0.5 text-[11.5px] text-faint">
          {footer}
        </div>
      ) : null}
    </div>
  )
}

/**
 * AllocationLine is one ceiling on one node, on one line: name, the track with
 * reserved filled and live use as a mark, the percentage, the amount.
 */
export function AllocationLine({
  label,
  dimension,
  format,
  usageAvailable,
}: {
  label: string
  dimension: CapacityDimension
  format: (value: number) => string
  usageAvailable: boolean
}) {
  const measured = dimension.allocatable > 0
  const used = usageAvailable && dimension.used > 0 ? dimension.used_percent : null
  const title = measured
    ? [
        `${label}: ${format(dimension.requested)} of ${format(dimension.allocatable)} reserved`,
        used !== null ? `${format(dimension.used)} in use` : 'live use not measured',
        dimension.limited > 0
          ? `limits ${percentText(dimension.limited_percent)} of the node`
          : 'no limits declared',
      ].join(' · ')
    : `${label}: the node reports no allocatable capacity`
  return (
    <TrackLine
      label={label}
      percent={measured ? dimension.requested_percent : null}
      mark={used}
      detail={measured ? `${format(dimension.requested)} / ${format(dimension.allocatable)}` : ''}
      title={title}
    />
  )
}

export function SlotLine({ slots }: { slots: PodSlots }) {
  const measured = slots.allocatable > 0
  return (
    <TrackLine
      label="Pods"
      percent={measured ? slots.percent : null}
      mark={null}
      detail={measured ? `${slots.scheduled} / ${slots.allocatable}` : ''}
      title={
        `Pod slots: ${slots.scheduled} of ${measured ? slots.allocatable : 'unknown'} taken` +
        (slots.without_requests > 0 ? ` · ${slots.without_requests} reserving nothing` : '')
      }
    />
  )
}

function TrackLine({
  label,
  percent,
  mark,
  detail,
  title,
}: {
  label: string
  percent: number | null
  mark: number | null
  detail: string
  title: string
}) {
  const tone = percent === null ? 'ok' : allocationTone(percent)
  return (
    <div
      title={title}
      className="grid grid-cols-[56px_minmax(0,1fr)_40px] items-center gap-x-2.5 sm:grid-cols-[56px_minmax(0,1fr)_40px_160px]"
    >
      <span className="text-[12px] text-muted">{label}</span>
      <span
        role="meter"
        aria-label={`${label} reserved`}
        aria-valuenow={percent === null ? undefined : Math.round(percent)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={title}
        className="relative h-2 overflow-hidden rounded-full bg-raised"
      >
        {percent !== null ? (
          <span
            aria-hidden="true"
            className={`block h-full rounded-full ${FILL[tone]}`}
            style={{ width: `${clampPercent(percent)}%` }}
          />
        ) : (
          <span
            aria-hidden="true"
            className="block h-full w-full text-faint opacity-30"
            style={{
              backgroundImage:
                'repeating-linear-gradient(135deg, currentColor 0 2px, transparent 2px 6px)',
            }}
          />
        )}
        {mark !== null ? (
          <span
            aria-hidden="true"
            className="absolute inset-y-0 w-0.5 bg-fg"
            style={{ left: `calc(${clampPercent(mark)}% - 1px)` }}
          />
        ) : null}
      </span>
      <span
        className={`text-right font-data text-[12.5px] font-semibold tabular-nums ${
          percent === null ? 'text-faint' : TEXT[tone]
        }`}
      >
        {percent === null ? '—' : percentText(percent)}
      </span>
      <span className="hidden truncate text-right font-data text-[12px] text-faint tabular-nums sm:block">
        {detail}
      </span>
    </div>
  )
}

/**
 * SegmentBar is a count split into its parts — node states, QoS classes — as
 * one stacked track with its legend under it. `compact` drops the legend to
 * the tooltip, for a table cell.
 */
export function SegmentBar({
  label,
  segments,
  compact = false,
  empty = 'none',
}: {
  label: string
  segments: readonly Segment[]
  compact?: boolean
  /** What an empty bar says. */
  empty?: string
}) {
  const shares = segmentShares(segments)
  const text = shares.length
    ? shares.map((segment) => `${segment.value} ${segment.label}`).join(' · ')
    : empty
  return (
    <div className="min-w-0" title={`${label}: ${text}`}>
      <div
        role="img"
        aria-label={`${label}: ${text}`}
        className={`flex overflow-hidden rounded-full bg-raised ${compact ? 'h-1.5' : 'h-2'} gap-px`}
      >
        {shares.map((segment) => (
          <span
            key={segment.key}
            aria-hidden="true"
            className={`block h-full ${segment.fill}`}
            style={{ width: `${segment.share}%` }}
          />
        ))}
      </div>
      {compact ? null : (
        <ul className="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[11.5px] text-muted">
          {segments.map((segment) => (
            <li
              key={segment.key}
              className={`inline-flex items-center gap-1.5 ${segment.value > 0 ? '' : 'opacity-50'}`}
            >
              <span aria-hidden="true" className={`size-2 rounded-full ${segment.fill}`} />
              {segment.label}
              <span className="font-data text-fg tabular-nums">{segment.value}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/**
 * FitBar is the fragmentation reading for one resource: the track is what is
 * free across every node that takes pods, the fill is the most one node can
 * take. A short fill is capacity that only exists in sum.
 */
export function FitBar({
  label,
  share,
  largest,
  free,
  node,
}: {
  label: string
  share: number
  largest: string
  free: string
  node: string
}) {
  const title = `${label}: the largest single node (${node}) can take ${largest} of ${free} free in total`
  return (
    <div title={title} className="min-w-0">
      <div className="flex items-baseline gap-2 text-[12px]">
        <span className="text-muted">{label}</span>
        <span className="ml-auto font-data text-fg tabular-nums">{largest}</span>
        <span className="font-data text-faint tabular-nums">/ {free}</span>
      </div>
      <div
        role="meter"
        aria-label={`${label} largest single-node fit`}
        aria-valuenow={Math.round(share)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuetext={title}
        className="mt-1 h-2 overflow-hidden rounded-full bg-raised"
      >
        <span
          aria-hidden="true"
          className="block h-full rounded-full bg-chart-1"
          style={{ width: `${clampPercent(share)}%` }}
        />
      </div>
      <p className="mt-0.5 truncate font-data text-[11.5px] text-faint">{node}</p>
    </div>
  )
}

/** ShareRow is one pod in a ranked list, with a bar against the list's largest. */
export function ShareRow({
  namespace,
  name,
  share,
  figure,
  detail,
}: {
  namespace: string
  name: string
  /** 0–100, against the largest in the list. */
  share: number
  /** The ranked figure, already formatted. */
  figure: string
  detail: string
}) {
  return (
    <li
      title={`${namespace}/${name} — ${detail}`}
      className="grid grid-cols-[minmax(0,1fr)_minmax(60px,30%)_44px] items-center gap-x-2.5 font-data text-[12px]"
    >
      <span className="min-w-0 truncate">
        <span className="text-faint">{namespace}/</span>
        <span className="text-fg">{name}</span>
      </span>
      <span aria-hidden="true" className="h-1.5 overflow-hidden rounded-full bg-raised">
        <span className="block h-full rounded-full bg-muted" style={{ width: `${share}%` }} />
      </span>
      <span className="text-right text-muted tabular-nums">{figure}</span>
    </li>
  )
}
