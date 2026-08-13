import { AlertTriangle, CheckCircle2, Info, ShieldCheck } from 'lucide-react'
import type { SetupCheck } from '../../api/types'
import { CodeBlock } from '../primitives'

/*
 * How this install was started, painted.
 *
 * It lives here rather than inside the setup wizard because the facts outlive
 * the wizard: a self-signed certificate and an unencrypted recording directory
 * are as true on the second month as on the first boot, and setup runs exactly
 * once. Both surfaces render the same rows from the same server-side checks, so
 * neither can drift into wording a remedy differently from the other.
 */

const CHECK_ICON = {
  ok: CheckCircle2,
  warn: AlertTriangle,
  blocked: ShieldCheck,
} as const

/* The same three tones Notice paints, on a card rather than a line — a check
   carries a title, an explanation and often a command, which is more than one
   paragraph's worth. */
const CHECK_TONE = {
  ok: 'border-ok/35 bg-ok-soft text-ok',
  warn: 'border-warn/35 bg-warn-soft text-warn',
  blocked: 'border-danger/35 bg-danger-soft text-danger',
} as const

/** One check: what is true, why it matters, and the literal line that changes
    it. A check that passed carries no fix, and neither does one whose remedy is
    a field on the page it is shown next to. */
export function DeploymentCheck({ check }: { check: SetupCheck }) {
  const Icon = CHECK_ICON[check.severity] ?? Info
  const tone = CHECK_TONE[check.severity] ?? CHECK_TONE.ok

  return (
    <div className={`flex flex-col gap-2 rounded-card border p-3.5 ${tone}`}>
      <p className="flex items-center gap-2 text-[13px] font-medium">
        <Icon aria-hidden="true" className="size-4 shrink-0" />
        {check.title}
      </p>
      <p className="text-[12.5px] leading-relaxed text-muted">{check.detail}</p>
      {check.fix ? <CodeBlock label="To change it" value={check.fix} /> : null}
    </div>
  )
}

/** Every check, or the sentence that says there were none — which is not the
    same as an empty panel that might still be loading. */
export function DeploymentCheckList({ checks }: { checks: SetupCheck[] }) {
  if (checks.length === 0) {
    return <p className="text-[13px] text-muted">Nothing to report.</p>
  }
  return (
    <>
      {checks.map((check) => (
        <DeploymentCheck key={check.key} check={check} />
      ))}
    </>
  )
}
