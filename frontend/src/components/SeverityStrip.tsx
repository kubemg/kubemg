import { ShieldAlert, ShieldCheck } from 'lucide-react'
import type { PostureSeverity } from '../lib/posture'
import { StatTile } from './primitives'

/**
 * How loudly a band is drawn.
 *
 * These are the deck's semantic tokens and nothing new: rust for what owns the
 * node, amber for what widens its blast radius, sage for what is merely
 * undeclared. Lime is deliberately absent — it is the interactive accent, and a
 * severity is not something you press. What *is* pressable is the tile around
 * it, which takes the ordinary control treatment.
 */
export const SEVERITY_STYLE: Record<
  PostureSeverity,
  { label: string; text: string; bar: string; tone: 'danger' | 'warn' | 'neutral' }
> = {
  critical: { label: 'Critical', text: 'text-danger', bar: 'bg-danger', tone: 'danger' },
  high: { label: 'High', text: 'text-danger', bar: 'bg-danger/60', tone: 'danger' },
  medium: { label: 'Medium', text: 'text-warn', bar: 'bg-warn', tone: 'warn' },
  low: { label: 'Low', text: 'text-muted', bar: 'bg-faint', tone: 'neutral' },
}

/**
 * The distribution, above the list.
 *
 * 36 findings at identical visual weight is a list that can be read and not
 * worked through — the first question a security team asks is "how bad, and how
 * much", and it had to be answered by counting rows. Each tile carries both the
 * total and how many are still open, because they answer different questions:
 * the total is the shape of the cluster, the open count is the work. A fully
 * triaged cluster showing only totals would look permanently alarming, which is
 * how a page like this stops being read.
 *
 * The tiles are the severity filter. A distribution nobody can act on is a
 * decoration, and the rows a reader wants after seeing "3 critical" are those
 * three; pressing the lit tile again clears it.
 */
export function SeverityStrip({
  distribution,
  selected,
  onSelect,
}: {
  distribution: { severity: PostureSeverity; total: number; open: number }[]
  selected: PostureSeverity | null
  onSelect: (severity: PostureSeverity) => void
}) {
  const total = distribution.reduce((sum, band) => sum + band.total, 0)
  return (
    <div className="flex flex-col gap-3">
      {/* How bad and how much, at a glance, before the tiles that filter. */}
      {total > 0 ? (
        <div
          role="img"
          aria-label={distribution
            .filter((band) => band.total > 0)
            .map((band) => `${band.total} ${SEVERITY_STYLE[band.severity].label.toLowerCase()}`)
            .join(', ')}
          className="flex h-2 gap-0.5 overflow-hidden rounded-full bg-raised"
        >
          {distribution
            .filter((band) => band.total > 0)
            .map((band) => (
              <span
                key={band.severity}
                className={`block h-full ${SEVERITY_STYLE[band.severity].bar}`}
                style={{ flexGrow: band.total }}
              />
            ))}
        </div>
      ) : null}
      <div role="group" aria-label="Filter by severity" className="grid grid-cols-2 gap-4 xl:grid-cols-4">
        {distribution.map((band) => {
          const style = SEVERITY_STYLE[band.severity]
          return (
            <StatTile
              key={band.severity}
              icon={band.severity === 'low' ? ShieldCheck : ShieldAlert}
              label={style.label}
              value={band.total}
              tone={band.total === 0 ? 'neutral' : style.tone}
              sub={band.total > 0 ? (band.open === 0 ? 'all acknowledged' : `${band.open} open`) : undefined}
              pressed={selected === band.severity}
              disabled={band.total === 0}
              onClick={() => onSelect(band.severity)}
            />
          )
        })}
      </div>
    </div>
  )
}

/** A band's name, for a group heading. */
export function SeverityTag({ severity }: { severity: PostureSeverity }) {
  const style = SEVERITY_STYLE[severity]
  return (
    <span className="flex items-center gap-2">
      <span aria-hidden="true" className={`h-3 w-1 shrink-0 rounded-full ${style.bar}`} />
      <span className={`text-[12.5px] font-medium ${style.text}`}>{style.label}</span>
    </span>
  )
}
