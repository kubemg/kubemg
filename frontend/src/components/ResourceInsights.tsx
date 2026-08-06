/*
 * The pilot header: what a list is, above the list itself.
 *
 * It is one band rather than a dashboard, and that is the whole design
 * constraint — an operator opening Explore is on their way to a specific
 * object, so this has to answer "is anything wrong in here" in a glance and
 * then get out of the way. Everything in it is derived from rows already
 * loaded (`lib/insights.ts`), so it costs no read and cannot disagree with the
 * table under it.
 *
 * Two things keep it from growing into a panel. Empty buckets are not drawn at
 * all, so a healthy namespace shows two readings and one line; and the named
 * alerts are capped, with the remainder counted rather than listed — a header
 * that can grow to forty rows is a page, and the page is already below it.
 */

import { AlertTriangle } from 'lucide-react'
import type { InsightBucket, InsightStat, ResourceInsight } from '../lib/insights'
import { TONE_TEXT } from '../lib/status'
import { formatCPU, formatMemory } from '../lib/units'

export function ResourceInsights({
  insight,
  bucket,
  onBucket,
  onOpen,
}: {
  insight: ResourceInsight
  /** The bucket the list is currently narrowed to, or null for the whole list. */
  bucket: InsightBucket | null
  onBucket: (next: InsightBucket | null) => void
  /** Opens one alerting object, which is where a header full of names should lead. */
  onOpen?: (name: string, namespace: string) => void
}) {
  const { stats, alerts, alerting, usage } = insight
  const hidden = alerting - alerts.length

  return (
    <section className="card px-4 py-3">
      <div className="flex flex-wrap items-end gap-x-5 gap-y-3">
        {stats.map((stat) => (
          // The label is unique within one header, and it is what identifies a
          // reading to a person — two stats can share a bucket id (a workload's
          // total and its replica count both narrow to nothing in particular).
          <Stat
            key={stat.label}
            stat={stat}
            active={bucket !== null && stat.selectable && bucket === stat.id && stat.id !== 'all'}
            onSelect={
              stat.selectable
                ? () => onBucket(stat.id === 'all' || bucket === stat.id ? null : stat.id)
                : undefined
            }
          />
        ))}

        {usage ? (
          <div className="ml-auto text-right">
            <p className="label">In use</p>
            <p className="mt-1 font-mono text-[13px] text-fg tabular-nums">
              {formatCPU(usage.cpu)} · {formatMemory(usage.memory)}
            </p>
            <p className="mt-0.5 text-[11px] text-faint">
              sampled on {usage.sampled} of {stats[0].value}
            </p>
          </div>
        ) : null}
      </div>

      <div className="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1.5 border-t border-line-soft pt-2.5">
        {/* The headline stays put while the list revalidates: the card below
            already says the cluster is being read, and replacing a true summary
            with a progress note every few seconds is the flicker that makes a
            header not worth looking at. */}
        <p className="text-[12px] text-muted">{insight.headline}</p>

        {alerts.length > 0 ? (
          <>
            <AlertTriangle
              aria-hidden="true"
              className={`size-3.5 shrink-0 ${alerts[0].tone === 'bad' ? 'text-danger' : 'text-warn'}`}
            />
            {alerts.map((alert) =>
              onOpen ? (
                <button
                  key={alert.key}
                  type="button"
                  onClick={() => onOpen(alert.name, alert.namespace)}
                  title={`${alert.namespace}/${alert.name} — ${alert.reason}`}
                  className={`max-w-full rounded-chip px-2 py-0.5 text-[12px] transition-colors ${
                    alert.tone === 'bad'
                      ? 'bg-danger-soft text-danger hover:bg-danger-soft/70'
                      : 'bg-warn-soft text-warn hover:bg-warn-soft/70'
                  }`}
                >
                  <span className="font-mono">{alert.name}</span>
                  <span className="opacity-80"> · {alert.reason}</span>
                </button>
              ) : (
                <span
                  key={alert.key}
                  title={`${alert.namespace}/${alert.name} — ${alert.reason}`}
                  className={`rounded-chip px-2 py-0.5 text-[12px] ${
                    alert.tone === 'bad' ? 'bg-danger-soft text-danger' : 'bg-warn-soft text-warn'
                  }`}
                >
                  <span className="font-mono">{alert.name}</span>
                  <span className="opacity-80"> · {alert.reason}</span>
                </span>
              ),
            )}
            {hidden > 0 ? (
              <span className="text-[12px] text-faint">and {hidden} more</span>
            ) : null}
          </>
        ) : null}
      </div>
    </section>
  )
}

/**
 * One reading. It is a button wherever clicking it narrows the list, because a
 * count somebody is looking at is almost always a count they want the rows for;
 * a reading with no rows behind it (replicas) stays plain text rather than
 * becoming a control that does nothing.
 */
function Stat({
  stat,
  active,
  onSelect,
}: {
  stat: InsightStat
  active: boolean
  onSelect?: () => void
}) {
  const value = (
    <>
      <span
        className={`block font-mono text-[22px] leading-none font-semibold tabular-nums ${
          stat.tone ? TONE_TEXT[stat.tone] : 'text-fg'
        }`}
      >
        {stat.value}
      </span>
      <span className="label mt-1.5 block whitespace-nowrap">{stat.label}</span>
      {stat.detail ? (
        <span className="mt-0.5 block text-[11px] whitespace-nowrap text-faint">{stat.detail}</span>
      ) : null}
    </>
  )

  if (!onSelect) return <div className="min-w-0 px-1">{value}</div>

  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      className={`min-w-0 rounded-control px-1.5 py-1 text-left transition-colors ${
        active ? 'bg-accent-soft' : 'hover:bg-raised'
      }`}
    >
      {value}
    </button>
  )
}
