import { CircleAlert, CircleCheck, CircleDashed } from 'lucide-react'
import type { ResourceCondition } from '../api/types'
import type { Glance, GlanceChip } from '../lib/objectGlance'
import { conditionTone, replicaTone } from '../lib/objectGlance'
import { TONE_FILL, TONE_SOFT, TONE_TEXT } from '../lib/status'
import type { Tone } from '../lib/status'
import { Age } from './primitives'

/*
 * The drawer's at-a-glance band: an object's state drawn rather than written.
 * Every figure here is also in the Describe tab; this is where the eye lands
 * first. The arithmetic is `lib/objectGlance.ts`'s, so it can be pinned.
 */

/* Past this many replicas the marks would be dust; the figure carries it. */
const MAX_MARKS = 48

function ReplicaMarks({ desired, ready, noun }: { desired: number; ready: number; noun: string }) {
  const tone = replicaTone(ready, desired)
  const marks = Math.min(desired, MAX_MARKS)
  const lit = Math.min(ready, marks)
  const label = `${ready} of ${desired} ${noun}${desired === 1 ? '' : 's'} ready`
  return (
    <div className="flex min-w-0 items-center gap-4">
      <div className="shrink-0">
        <p className={`font-data text-[28px] leading-none font-bold tabular-nums ${tone === 'ok' || tone === 'idle' ? 'text-fg' : TONE_TEXT[tone]}`}>
          {ready}
          <span className="text-[18px] font-semibold text-faint">/{desired}</span>
        </p>
        <p className="mt-1 text-[12px] text-muted">ready</p>
      </div>
      <div role="img" aria-label={label} title={label} className="flex min-w-0 flex-wrap gap-1.5">
        {desired === 0 ? (
          <span className="text-[12px] text-faint">scaled to zero</span>
        ) : (
          Array.from({ length: marks }, (_, index) => (
            <span
              key={index}
              aria-hidden="true"
              className={`h-5 w-2.5 rounded-full ${index < lit ? TONE_FILL[tone] : 'border border-dashed border-faint'}`}
            />
          ))
        )}
        {desired > MAX_MARKS ? (
          <span className="self-center font-data text-[12px] text-faint">+{desired - MAX_MARKS}</span>
        ) : null}
      </div>
    </div>
  )
}

function ProgressBar({ label, value, of }: { label: string; value: number; of: number }) {
  const percent = of > 0 ? Math.min(100, (value / of) * 100) : 0
  const tone: Tone = of === 0 ? 'idle' : value >= of ? 'ok' : 'warn'
  return (
    <div className="grid grid-cols-[92px_minmax(0,1fr)_48px] items-center gap-2.5" title={`${label}: ${value} of ${of}`}>
      <span className="text-[12px] text-muted">{label}</span>
      <span
        role="meter"
        aria-label={label}
        aria-valuenow={value}
        aria-valuemin={0}
        aria-valuemax={of}
        className="h-1.5 overflow-hidden rounded-full bg-raised"
      >
        <span aria-hidden="true" className={`block h-full rounded-full ${TONE_FILL[tone]}`} style={{ width: `${percent}%` }} />
      </span>
      <span className="text-right font-data text-[12px] text-fg tabular-nums">
        {value}/{of}
      </span>
    </div>
  )
}

function Chips({ chips }: { chips: readonly GlanceChip[] }) {
  if (chips.length === 0) return null
  return (
    <ul className="flex flex-wrap gap-1.5">
      {chips.map((chip) => (
        <li
          key={chip.label}
          className={`flex max-w-full min-w-0 items-center overflow-hidden rounded-chip border text-[12px] ${
            chip.tone && chip.tone !== 'idle' ? 'border-transparent' : 'border-line'
          }`}
        >
          <span className="shrink-0 bg-raised px-2 py-1 text-muted">{chip.label}</span>
          <span
            className={`truncate px-2 py-1 font-medium ${chip.data ? 'font-data' : ''} ${
              chip.tone ? TONE_SOFT[chip.tone] : 'text-fg'
            }`}
            title={chip.value}
          >
            {chip.value}
          </span>
        </li>
      ))}
    </ul>
  )
}

export function ObjectGlance({ glance }: { glance: Glance }) {
  const { replicas, bars, segments, chips } = glance
  const hasPicture = Boolean(replicas || segments || bars.length > 0)
  return (
    <div className="flex flex-col gap-4">
      {hasPicture ? (
        <div className="grid gap-x-8 gap-y-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          {replicas ? <ReplicaMarks {...replicas} /> : null}
          {segments ? <SegmentStrip {...segments} /> : null}
          {bars.length > 0 ? (
            <div className="flex min-w-0 flex-col justify-center gap-2">
              {bars.map((bar) => (
                <ProgressBar key={bar.label} {...bar} />
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
      <Chips chips={chips} />
    </div>
  )
}

function SegmentStrip({ label, total, parts }: NonNullable<Glance['segments']>) {
  const sum = Math.max(total, parts.reduce((acc, part) => acc + part.value, 0))
  return (
    <div className="min-w-0 md:col-span-2">
      <p className="mb-2 font-data text-[13px] font-semibold text-fg tabular-nums">{label}</p>
      <div role="img" aria-label={label} className="flex h-3 gap-px overflow-hidden rounded-full bg-raised">
        {sum > 0
          ? parts
              .filter((part) => part.value > 0)
              .map((part) => (
                <span
                  key={part.key}
                  aria-hidden="true"
                  title={`${part.label}: ${part.value}`}
                  className={`block h-full ${part.fill}`}
                  style={{ width: `${(part.value / sum) * 100}%` }}
                />
              ))
          : null}
      </div>
      <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted">
        {parts.map((part) => (
          <li key={part.key} className={`inline-flex items-center gap-1.5 ${part.value > 0 ? '' : 'opacity-50'}`}>
            <span aria-hidden="true" className={`size-2 rounded-full ${part.fill}`} />
            {part.label}
            <span className="font-data text-fg tabular-nums">{part.value}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

const CONDITION_GLYPH = { ok: CircleCheck, bad: CircleAlert, warn: CircleDashed } as const

/**
 * Conditions as lights: one tile each, coloured by whether the condition is
 * the healthy way up. The controller's sentence is the second line, cut to
 * one, and whole on the pointer.
 */
export function ConditionTiles({ conditions }: { conditions: readonly ResourceCondition[] }) {
  return (
    <ul className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-2">
      {conditions.map((condition) => {
        const tone = conditionTone(condition)
        const Glyph = CONDITION_GLYPH[tone === 'ok' ? 'ok' : tone === 'bad' ? 'bad' : 'warn']
        return (
          <li
            key={condition.type}
            title={[condition.reason, condition.message].filter(Boolean).join(' — ') || condition.type}
            className={`flex min-w-0 items-start gap-2.5 rounded-control border px-3 py-2.5 ${
              tone === 'ok' ? 'border-line-soft bg-sunken' : 'border-transparent ' + TONE_SOFT[tone]
            }`}
          >
            <Glyph aria-hidden="true" className={`mt-0.5 size-4 shrink-0 ${TONE_TEXT[tone]}`} />
            <span className="min-w-0 flex-1">
              <span className="flex items-baseline gap-2">
                <span className={`truncate font-data text-[12.5px] font-semibold ${tone === 'ok' ? 'text-fg' : ''}`}>
                  {condition.type}
                </span>
                <span className={`ml-auto shrink-0 text-[11.5px] ${tone === 'ok' ? 'text-faint' : ''}`}>
                  {condition.last_transition_at ? <Age iso={condition.last_transition_at} /> : null}
                </span>
              </span>
              <span className={`block truncate text-[11.5px] ${tone === 'ok' ? 'text-muted' : ''}`}>
                {condition.reason || condition.status}
              </span>
            </span>
          </li>
        )
      })}
    </ul>
  )
}
