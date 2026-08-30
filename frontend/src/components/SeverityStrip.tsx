import type { PostureSeverity } from '../lib/posture'

/**
 * How loudly a band is drawn.
 *
 * These are the deck's semantic tokens and nothing new: rust for what owns the
 * node, amber for what widens its blast radius, sage for what is merely
 * undeclared. Lime is deliberately absent — it is the interactive accent, and a
 * severity is not something you press. What *is* pressable is the tile around
 * it, which takes the ordinary control treatment.
 */
export const SEVERITY_STYLE: Record<PostureSeverity, { label: string; text: string; bar: string }> = {
  critical: { label: 'Critical', text: 'text-danger', bar: 'bg-danger' },
  high: { label: 'High', text: 'text-danger', bar: 'bg-danger/60' },
  medium: { label: 'Medium', text: 'text-warn', bar: 'bg-warn' },
  low: { label: 'Low', text: 'text-muted', bar: 'bg-faint' },
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
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      {distribution.map((band) => {
        const style = SEVERITY_STYLE[band.severity]
        const active = selected === band.severity
        return (
          <button
            key={band.severity}
            type="button"
            aria-pressed={active}
            disabled={band.total === 0}
            onClick={() => onSelect(band.severity)}
            className={`flex min-w-0 flex-col gap-1 rounded-card border p-3 text-left transition-colors disabled:cursor-default disabled:opacity-60 ${
              active
                ? "border-accent-line bg-accent-soft"
                : "border-line bg-surface enabled:hover:border-faint/60 enabled:hover:bg-raised"
            }`}
          >
            <span className="flex items-center gap-2">
              <span aria-hidden="true" className={`h-3 w-1 shrink-0 rounded-full ${style.bar}`} />
              <span className="label text-faint">{style.label}</span>
            </span>
            <span className="flex items-baseline gap-2">
              <span className={`font-mono text-[20px] leading-none font-semibold ${style.text}`}>
                {band.total}
              </span>
              {band.total > 0 ? (
                <span className="text-[12px] text-muted">
                  {band.open === 0 ? "all acknowledged" : `${band.open} open`}
                </span>
              ) : null}
            </span>
          </button>
        )
      })}
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
