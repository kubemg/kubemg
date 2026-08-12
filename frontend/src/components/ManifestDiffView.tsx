import type { ManifestDiff, ManifestDiffChange } from '../api/types'
import { Notice } from './primitives'

/**
 * The one renderer for a structural diff between two decoded objects — the
 * manifest editor's pre-write confirmation and the audit trail's stored
 * `update` row both show exactly this component over exactly the shape
 * `pkg/objdiff.Result` produces on the wire. Keeping it to one component
 * means the diff an operator approves before clicking Apply and the diff
 * that turns up later in the trail read identically, which is the whole
 * point of computing both from the same backend function.
 *
 * It is a table rather than a unified-diff-style block because there is no
 * text here to align — `added`/`removed`/`changed` already says what
 * happened at a path, and putting the old and new value in their own columns
 * reads left-to-right the way a person actually compares two things, rather
 * than asking them to spot a `-`/`+` in front of a wrapped JSON blob.
 */

const KIND_LABEL: Record<ManifestDiffChange['kind'], string> = {
  added: 'added',
  removed: 'removed',
  changed: 'changed',
}

const KIND_TONE: Record<ManifestDiffChange['kind'], string> = {
  added: 'text-ok',
  removed: 'text-danger',
  changed: 'text-warn',
}

/** renderValue turns a decoded JSON value into something worth reading in a
    monospace cell. A string prints bare — quoting "web:1.2" as JSON would add
    noise a person reading a diff does not want — everything else is its JSON
    form, since that is the only unambiguous way to show a number, a bool, or
    a nested object/array in one line. */
function renderValue(value: unknown): string {
  if (value === undefined) return '—'
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

export function ManifestDiffView({
  diff,
  emptyLabel = 'No differences from the object on the cluster.',
}: {
  diff: ManifestDiff
  /** Shown when the two objects compare equal — a real answer, not a loading
      state, so it is worth letting the caller phrase it for its own context. */
  emptyLabel?: string
}) {
  if (diff.changes.length === 0) {
    return <p className="text-[13px] text-muted">{emptyLabel}</p>
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="max-h-80 overflow-auto rounded-control border border-line-soft">
        <table className="w-full border-collapse text-[12.5px]">
          <thead>
            <tr className="border-b border-line-soft bg-raised text-left">
              <th className="px-3 py-2 font-medium text-muted">Field</th>
              <th className="px-3 py-2 font-medium text-muted">Before</th>
              <th className="px-3 py-2 font-medium text-muted">After</th>
            </tr>
          </thead>
          <tbody>
            {diff.changes.map((change) => (
              <tr key={change.path} className="border-b border-line-soft last:border-0">
                <td className="px-3 py-2 align-top font-mono text-[12px] text-fg">
                  <span
                    className={`mr-1.5 font-sans text-[10.5px] font-semibold uppercase tracking-wide ${KIND_TONE[change.kind]}`}
                  >
                    {KIND_LABEL[change.kind]}
                  </span>
                  <span className="break-all">{change.path}</span>
                </td>
                <td className="max-w-[280px] px-3 py-2 align-top font-mono text-[12px] break-all text-danger/90">
                  {change.kind === 'added' ? '—' : renderValue(change.old)}
                </td>
                <td className="max-w-[280px] px-3 py-2 align-top font-mono text-[12px] break-all text-ok/90">
                  {change.kind === 'removed' ? '—' : renderValue(change.new)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {diff.truncated ? (
        <Notice tone="warn">
          This object differs in more places than fit here — the list above is a prefix of the real
          diff, not a sample of it.
        </Notice>
      ) : null}
    </div>
  )
}
