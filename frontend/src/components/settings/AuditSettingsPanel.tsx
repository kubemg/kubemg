import { Info, ShieldCheck } from 'lucide-react'
import type { RuntimeSettings } from '../../api/types'
import { Chip, Field, Notice, Panel, TextInput } from '../primitives'

/**
 * What the audit trail keeps, and for how long.
 *
 * Two controls that look like preferences and are not. The verb selection
 * decides what a queryable table is worth on a busy fleet — on a large one,
 * `list` and `get` are the overwhelming majority of the rows and almost never
 * the rows anybody reads — and the retention pair decides how long the evidence
 * exists at all.
 *
 * The panel's job beyond collecting the values is to be honest about the floor:
 * a selection cannot suppress a refusal, an interactive session, or KubeMG's own
 * records of who watched a recording. Saying so here matters because an operator
 * unticking `delete` would otherwise reasonably believe deletions had gone
 * untracked, and they have not.
 */

/** The verbs the selection governs, in the order an operator thinks about them:
    the reads that dominate the volume first, then the writes, then the sessions. */
const VERB_GROUPS: Array<{ label: string; hint: string; verbs: string[] }> = [
  {
    label: 'Reads',
    hint: 'The bulk of the trail on any active fleet, and the rows least often read back.',
    verbs: ['list', 'get', 'watch', 'log'],
  },
  {
    label: 'Writes',
    hint: 'What changed, and who changed it.',
    verbs: ['create', 'update', 'patch', 'delete'],
  },
  {
    label: 'Sessions',
    hint: 'Interactive access. Recorded whatever is selected here — see below.',
    verbs: ['exec', 'attach', 'portforward'],
  },
]

export function AuditSettingsPanel({
  settings,
  selectedVerbs,
  onVerbsChange,
  recordSessions,
  onRecordSessionsChange,
  recordManifestDiffs,
  onRecordManifestDiffsChange,
  retentionDays,
  onRetentionChange,
  recordingRetentionDays,
  onRecordingRetentionChange,
  retentionError,
  recordingRetentionError,
}: {
  settings: { effective: RuntimeSettings; defaults: RuntimeSettings }
  /** Null means no selection is in force, which records every verb. */
  selectedVerbs: string[] | null
  onVerbsChange: (next: string[] | null) => void
  recordSessions: boolean
  onRecordSessionsChange: (next: boolean) => void
  /** Whether an `update` row keeps the manifest diff it wrote. Off by default. */
  recordManifestDiffs: boolean
  onRecordManifestDiffsChange: (next: boolean) => void
  retentionDays: string
  onRetentionChange: (next: string) => void
  recordingRetentionDays: string
  onRecordingRetentionChange: (next: string) => void
  retentionError?: string
  recordingRetentionError?: string
}) {
  const { effective, defaults } = settings
  const selecting = selectedVerbs !== null

  // With no selection every verb is recorded, so every chip reads as on — which
  // is what makes the first click a *narrowing* rather than an inversion.
  const active = (verb: string) => !selecting || selectedVerbs.includes(verb)

  function toggle(verb: string) {
    if (!selecting) {
      // The first click starts a selection from "everything except this one",
      // because that is what the operator just asked for.
      const all = VERB_GROUPS.flatMap((group) => group.verbs)
      onVerbsChange(all.filter((entry) => entry !== verb))
      return
    }
    const next = selectedVerbs.includes(verb)
      ? selectedVerbs.filter((entry) => entry !== verb)
      : [...selectedVerbs, verb]
    // Back to everything ticked is the same thing as no selection, and storing it
    // as one keeps a fleet that later gains a verb recording it by default.
    const all = VERB_GROUPS.flatMap((group) => group.verbs)
    onVerbsChange(next.length === all.length ? null : next)
  }

  return (
    <>
      <Panel
        eyebrow="Audit"
        title="What reaches the trail"
        description="Every proxied call is recorded. On a busy fleet most of those rows are reads nobody queries, so the table can be narrowed to the actions worth keeping — the structured log a SIEM tails stays complete either way."
        bodyClassName="flex flex-col gap-4 p-4"
        actions={
          selecting ? (
            <button
              type="button"
              onClick={() => onVerbsChange(null)}
              className="text-[12.5px] text-accent hover:underline"
            >
              Record everything
            </button>
          ) : null
        }
      >
        {VERB_GROUPS.map((group) => (
          <div key={group.label} className="flex flex-col gap-2">
            <p className="label">{group.label}</p>
            <div className="flex flex-wrap gap-2">
              {group.verbs.map((verb) => (
                <Chip key={verb} active={active(verb)} onClick={() => toggle(verb)}>
                  <span className="font-mono text-[12.5px]">{verb}</span>
                </Chip>
              ))}
            </div>
            <p className="text-[12px] leading-snug text-muted">{group.hint}</p>
          </div>
        ))}

        {/* The floor. It is the reason this control is a volume knob rather than
            a way to act unobserved, and an operator has to be able to read it
            before they untick anything. */}
        <Notice tone="info">
          <span className="inline-flex items-baseline gap-1.5">
            <ShieldCheck aria-hidden="true" className="size-3.5 translate-y-0.5" />
            <span>
              Three things are recorded whatever is selected: anything kubemg{' '}
              <strong>refused</strong> or that failed, every <strong>interactive session</strong>{' '}
              while it is open, and who <strong>replayed or deleted</strong> a recording. Turning a
              verb off reduces volume; it cannot hide an action.
            </span>
          </span>
        </Notice>

        {!selecting ? (
          <p className="flex items-baseline gap-1.5 text-[12px] text-muted">
            <Info aria-hidden="true" className="size-3.5 translate-y-0.5 shrink-0" />
            Every verb is recorded. Untick one to start narrowing.
          </p>
        ) : null}
      </Panel>

      <Panel
        eyebrow="Retention"
        title="How long it is kept"
        description="A background pass twice a day deletes anything past these windows. Deletions are permanent — there is no archive behind them."
        bodyClassName="flex flex-col gap-4 p-4"
      >
        <Field
          label="Audit trail (days)"
          htmlFor="audit_retention_days"
          hint={`Records older than this are deleted. Leave empty for ${defaults.audit_retention_days} days.`}
          error={retentionError}
        >
          <TextInput
            id="audit_retention_days"
            type="number"
            min={1}
            max={3650}
            step={1}
            inputMode="numeric"
            className="max-w-40 font-mono text-[12.5px]"
            placeholder={String(defaults.audit_retention_days)}
            value={retentionDays}
            onChange={(event) => onRetentionChange(event.target.value)}
          />
        </Field>
        <Effective label="In use" value={`${effective.audit_retention_days} days`} />

        <Field
          label="Session recordings (days)"
          htmlFor="session_recording_retention_days"
          hint={`Recordings are far larger than the rows describing them, so this is usually shorter. Leave empty to follow the audit window; it can never be longer than it, because a replay must not outlive the record saying the shell was opened.`}
          error={recordingRetentionError}
        >
          <TextInput
            id="session_recording_retention_days"
            type="number"
            min={1}
            max={3650}
            step={1}
            inputMode="numeric"
            className="max-w-40 font-mono text-[12.5px]"
            placeholder={String(effective.audit_retention_days)}
            value={recordingRetentionDays}
            onChange={(event) => onRecordingRetentionChange(event.target.value)}
          />
        </Field>
        <Effective
          label="In use"
          value={`${effective.session_recording_retention_days} days`}
        />

        {/* This lives beside retention rather than beside the exec-session
            switch below: it is governed by the same window, deleted the same
            way, and the choice to make here is exactly "how long is this
            evidence worth keeping", not "should this feature run". */}
        <label className="flex items-start gap-2.5 border-t border-line-soft pt-4">
          <input
            type="checkbox"
            className="mt-0.5 size-4 accent-[var(--color-accent)]"
            checked={recordManifestDiffs}
            onChange={(event) => onRecordManifestDiffsChange(event.target.checked)}
          />
          <span className="min-w-0">
            <span className="text-[13px] text-fg">Keep the field-level diff of manifest writes</span>
            <span className="mt-0.5 block text-[12px] leading-snug text-muted">
              An <code>update</code> row already says a manifest was applied; this makes it say
              what changed. Off by default, because a manifest body can hold values as sensitive as
              a Secret's without the object being one — an inlined token in a ConfigMap, a
              certificate in a Deployment's env — so this is a decision to retain more, not a
              cosmetic toggle. A Secret's own diff is never stored, whatever this is set to, and the
              stored diff is pruned by the retention window above like every other row.
            </span>
          </span>
        </label>
        <Effective
          label="Right now"
          value={effective.record_manifest_diffs ? 'kept' : 'not kept'}
        />
      </Panel>

      <Panel
        eyebrow="Recording"
        title="Interactive session capture"
        description="Every exec and attach through the proxy is teed into an asciinema recording, so the trail can say not only that a shell was opened in production but what was done in it."
        bodyClassName="flex flex-col gap-4 p-4"
      >
        {effective.recording_available ? (
          <>
            <label className="flex items-start gap-2.5">
              <input
                type="checkbox"
                className="mt-0.5 size-4 accent-[var(--color-accent)]"
                checked={recordSessions}
                onChange={(event) => onRecordSessionsChange(event.target.checked)}
              />
              <span className="min-w-0">
                <span className="text-[13px] text-fg">Record exec and attach sessions</span>
                <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                  Turning this off stops the next session from being recorded and leaves the ones
                  already running alone. Sessions are still audited either way — what is lost is
                  the replay.
                </span>
              </span>
            </label>
            <Effective
              label="Right now"
              value={effective.record_exec_sessions ? 'recording' : 'not recording'}
            />
          </>
        ) : (
          // A switch that silently does nothing is worse than one that says why.
          <Notice tone="warn">
            This server was started without a recording directory, so nothing can be recorded and
            this setting cannot turn it on. Set <code>KUBEMG_SESSION_RECORDING_DIR</code> to a
            mounted volume and restart — recordings have to outlive the container.
          </Notice>
        )}
      </Panel>
    </>
  )
}

function Effective({ label, value }: { label: string; value: string }) {
  return (
    <p className="flex flex-wrap items-baseline gap-2 rounded-control bg-raised px-3 py-2">
      <span className="label">{label}</span>
      <span className="min-w-0 truncate font-mono text-[12.5px] text-fg">{value}</span>
    </p>
  )
}
