import { Circle, Lock, ShieldAlert } from 'lucide-react'
import type { RecordingPolicy } from '../../api/types'

/**
 * RecordingNotice tells an operator what is being captured, before they type.
 *
 * It is a line rather than a dialog on purpose: a shell that has to be
 * acknowledged before it opens would be dismissed by reflex within a week, and
 * the point is that the fact stays visible for the whole session. What it says is
 * specific — keystrokes or only output, encrypted at rest or not, and for how
 * long — because "this session may be monitored" is the kind of notice people
 * stop reading.
 */
export function RecordingNotice({ policy }: { policy: RecordingPolicy }) {
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-control border border-line-soft bg-raised/60 px-2.5 py-1.5 text-[12px] text-muted">
      <Circle aria-hidden="true" className="size-3 shrink-0 fill-danger text-danger" />
      <span className="text-fg">This session is being recorded.</span>
      <span>
        {policy.input_recorded
          ? 'Output and keystrokes are captured'
          : 'Output is captured; keystrokes are not'}
        {policy.retention_days > 0 ? ` and kept for ${policy.retention_days} days` : ''}.
      </span>
      {policy.encrypted ? (
        <span className="inline-flex items-center gap-1 text-faint">
          <Lock aria-hidden="true" className="size-3 shrink-0" />
          encrypted at rest
        </span>
      ) : (
        /* Said plainly, because it changes what an operator should be willing to
           type: an unencrypted recording is a plaintext file holding whatever a
           prompt did not echo. */
        <span className="inline-flex items-center gap-1 text-warn">
          <ShieldAlert aria-hidden="true" className="size-3 shrink-0" />
          not encrypted at rest
        </span>
      )}
    </p>
  )
}
