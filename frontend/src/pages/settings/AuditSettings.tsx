import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../../api/client'
import type { SettingsResponse } from '../../api/types'
import { Button, Notice } from '../../components/primitives'
import { AuditForwardingPanel } from '../../components/settings/AuditForwardingPanel'
import { AuditSettingsPanel } from '../../components/settings/AuditSettingsPanel'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/**
 * Blank means "use the default", so the form state is the override, not the
 * value. Retention is a number in the API but is edited as a string for
 * exactly the same reason: empty has to stay distinguishable from zero.
 */
type Draft = {
  audit_retention_days: string
  session_recording_retention_days: string
  /** Null means no verb selection is in force, which records every verb. It is
      not the same as an empty array, and the API preserves the difference. */
  audit_verbs: string[] | null
  record_exec_sessions: boolean
  record_manifest_diffs: boolean
}

/** draftOf turns a settings response into the form's own shape. */
function draftOf(settings: SettingsResponse): Draft {
  const { overrides, effective } = settings
  return {
    // 0 is how the API says "unset", which the form shows as an empty box.
    audit_retention_days:
      overrides.audit_retention_days > 0 ? String(overrides.audit_retention_days) : '',
    session_recording_retention_days:
      overrides.session_recording_retention_days > 0
        ? String(overrides.session_recording_retention_days)
        : '',
    audit_verbs: overrides.audit_verbs_selected ? overrides.audit_verbs : null,
    // Recording follows the process, so the effective value is the truth here —
    // there is no "unset" state for a boolean to fall back to.
    record_exec_sessions: effective.record_exec_sessions,
    // Off by default and there is no process-level fallback either, so the
    // effective value is the whole truth for this one too.
    record_manifest_diffs: effective.record_manifest_diffs,
  }
}

/** sameVerbs compares two selections, treating "no selection" as its own value
    rather than as an empty one — they save differently. */
function sameVerbs(a: string[] | null, b: string[] | null): boolean {
  if (a === null || b === null) return a === b
  if (a.length !== b.length) return false
  const left = [...a].sort()
  const right = [...b].sort()
  return left.every((entry, index) => entry === right[index])
}

/** AuditSettings owns what the trail keeps, for how long, and whether shells
    are recorded. */
export function AuditSettings() {
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [draft, setDraft] = useState<Draft>({
    audit_retention_days: '',
    session_recording_retention_days: '',
    audit_verbs: null,
    record_exec_sessions: true,
    record_manifest_diffs: false,
  })
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const load = useCallback(async () => {
    try {
      const next = await fetchSettings()
      setSettings(next)
      setDraft(draftOf(next))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the settings.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const retentionDays = Number(draft.audit_retention_days.trim() || 0)
  const retentionValid =
    Number.isInteger(retentionDays) && retentionDays >= 0 && retentionDays <= 3650

  const recordingRetentionDays = Number(draft.session_recording_retention_days.trim() || 0)
  const recordingRetentionValid =
    Number.isInteger(recordingRetentionDays) &&
    recordingRetentionDays >= 0 &&
    recordingRetentionDays <= 3650

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      const next = await updateSettings({
        // An empty box clears the override, which the API spells as 0.
        audit_retention_days: retentionDays,
        session_recording_retention_days: recordingRetentionDays,
        // No selection is sent as an empty array, which the API reads as "clear
        // it back to every verb" rather than as "record nothing".
        audit_verbs: draft.audit_verbs ?? [],
        record_exec_sessions: draft.record_exec_sessions,
        record_manifest_diffs: draft.record_manifest_diffs,
      })
      setSettings(next)
      setDraft(draftOf(next))
      setSaved(true)
    } catch (err) {
      setError(errorMessage(err, 'Could not save the settings.'))
    } finally {
      setBusy(false)
    }
  }

  function set<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
    setSaved(false)
  }

  const dirty =
    settings !== null &&
    (retentionDays !== settings.overrides.audit_retention_days ||
      recordingRetentionDays !== settings.overrides.session_recording_retention_days ||
      !sameVerbs(
        draft.audit_verbs,
        settings.overrides.audit_verbs_selected ? settings.overrides.audit_verbs : null,
      ) ||
      draft.record_exec_sessions !== settings.effective.record_exec_sessions ||
      draft.record_manifest_diffs !== settings.effective.record_manifest_diffs)

  const valid = retentionValid && recordingRetentionValid

  return (
    <SettingsLayout
      title="Audit settings"
      actions={
        settings ? (
          <>
            <Button
              type="button"
              variant="ghost"
              disabled={busy || !dirty}
              onClick={() => {
                setDraft(draftOf(settings))
                setSaved(false)
              }}
            >
              <RotateCcw aria-hidden="true" className="size-4" />
              Discard
            </Button>
            <Button
              type="submit"
              form="audit-settings-form"
              variant="primary"
              disabled={busy || !dirty || !valid}
            >
              {busy ? 'Saving…' : 'Save settings'}
            </Button>
          </>
        ) : null
      }
    >
      <form id="audit-settings-form" onSubmit={save} className="flex min-w-0 max-w-3xl flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {settings?.warnings.map((warning) => (
          <Notice key={warning} tone="warn">
            {warning}
          </Notice>
        ))}
        {saved && !dirty ? <Notice tone="ok">Saved.</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {settings ? (
          <AuditSettingsPanel
            settings={settings}
            selectedVerbs={draft.audit_verbs}
            onVerbsChange={(next) => set('audit_verbs', next)}
            recordSessions={draft.record_exec_sessions}
            onRecordSessionsChange={(next) => set('record_exec_sessions', next)}
            recordManifestDiffs={draft.record_manifest_diffs}
            onRecordManifestDiffsChange={(next) => set('record_manifest_diffs', next)}
            retentionDays={draft.audit_retention_days}
            onRetentionChange={(next) => set('audit_retention_days', next)}
            recordingRetentionDays={draft.session_recording_retention_days}
            onRecordingRetentionChange={(next) => set('session_recording_retention_days', next)}
            retentionError={
              retentionValid ? undefined : 'Retention must be a whole number of days, up to 3650.'
            }
            recordingRetentionError={
              recordingRetentionValid
                ? undefined
                : 'Retention must be a whole number of days, up to 3650.'
            }
          />
        ) : null}
      </form>

      {/* Outside the form on purpose: a destination is saved the moment it is
          written, not by the page's Save button, and putting rows that already
          persisted behind a dirty-state save would be a lie about what is in
          force. */}
      <div className="mt-4 flex min-w-0 max-w-3xl flex-col gap-4">
        <AuditForwardingPanel />
      </div>
    </SettingsLayout>
  )
}
