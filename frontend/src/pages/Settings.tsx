import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../api/client'
import type { SettingsResponse } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, Field, Notice, Panel, TextInput } from '../components/primitives'
import { AlarmSettingsPanel } from '../components/settings/AlarmSettingsPanel'
import { AuditSettingsPanel } from '../components/settings/AuditSettingsPanel'
import { GuardrailSettingsPanel } from '../components/settings/GuardrailSettingsPanel'
import { SsoSettingsPanel } from '../components/SsoSettingsPanel'
import { useClusters } from '../state/clusters-context'

/**
 * Blank means "use the default", so the form state is the override, not the
 * value. Retention is a number in the API but is edited as a string for exactly
 * the same reason: empty has to stay distinguishable from zero.
 */
type Draft = {
  public_url: string
  agent_image: string
  agent_namespace: string
  audit_retention_days: string
  session_recording_retention_days: string
  /** Null means no verb selection is in force, which records every verb. It is
      not the same as an empty array, and the API preserves the difference. */
  audit_verbs: string[] | null
  record_exec_sessions: boolean
}

const EMPTY: Draft = {
  public_url: '',
  agent_image: '',
  agent_namespace: '',
  audit_retention_days: '',
  session_recording_retention_days: '',
  audit_verbs: null,
  record_exec_sessions: true,
}

const KEYS = ['public_url', 'agent_image', 'agent_namespace'] as const

/** draftOf turns a settings response into the form's own shape. */
function draftOf(settings: SettingsResponse): Draft {
  const { overrides, effective } = settings
  return {
    public_url: overrides.public_url,
    agent_image: overrides.agent_image,
    agent_namespace: overrides.agent_namespace,
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

export function Settings() {
  const { clusters } = useClusters()
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [draft, setDraft] = useState<Draft>(EMPTY)
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

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      const next = await updateSettings({
        public_url: draft.public_url.trim(),
        agent_image: draft.agent_image.trim(),
        agent_namespace: draft.agent_namespace.trim(),
        // An empty box clears the override, which the API spells as 0.
        audit_retention_days: retentionDays,
        session_recording_retention_days: recordingRetentionDays,
        // No selection is sent as an empty array, which the API reads as "clear
        // it back to every verb" rather than as "record nothing".
        audit_verbs: draft.audit_verbs ?? [],
        record_exec_sessions: draft.record_exec_sessions,
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

  function set(key: keyof Draft, value: Draft[keyof Draft]) {
    setDraft((current) => ({ ...current, [key]: value }))
    setSaved(false)
  }

  const retentionDays = Number(draft.audit_retention_days.trim() || 0)
  const retentionValid =
    Number.isInteger(retentionDays) && retentionDays >= 0 && retentionDays <= 3650

  const recordingRetentionDays = Number(draft.session_recording_retention_days.trim() || 0)
  const recordingRetentionValid =
    Number.isInteger(recordingRetentionDays) &&
    recordingRetentionDays >= 0 &&
    recordingRetentionDays <= 3650

  const dirty =
    settings !== null &&
    (KEYS.some((key) => draft[key].trim() !== settings.overrides[key]) ||
      retentionDays !== settings.overrides.audit_retention_days ||
      recordingRetentionDays !== settings.overrides.session_recording_retention_days ||
      !sameVerbs(
        draft.audit_verbs,
        settings.overrides.audit_verbs_selected ? settings.overrides.audit_verbs : null,
      ) ||
      draft.record_exec_sessions !== settings.effective.record_exec_sessions)

  const valid = retentionValid && recordingRetentionValid

  return (
    <AppShell
      title="Settings"
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
              form="settings-form"
              variant="primary"
              disabled={busy || !dirty || !valid}
            >
              {busy ? 'Saving…' : 'Save settings'}
            </Button>
          </>
        ) : null
      }
    >
      <form id="settings-form" onSubmit={save} className="flex min-w-0 max-w-3xl flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {settings?.warnings.map((warning) => (
          <Notice key={warning} tone="warn">
            {warning}
          </Notice>
        ))}
        {saved && !dirty ? <Notice tone="ok">Saved.</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {settings ? (
          <>
            <Panel
              eyebrow="Server"
              title="Where clusters reach KubeMG"
              description="Baked into every agent install command and into the agent's tunnel address, so it must be routable from inside a target cluster — not the address your browser uses."
              bodyClassName="flex flex-col gap-4 p-4"
            >
              <Field
                label="Server URL"
                htmlFor="public_url"
                hint={`Leave empty to use ${settings.defaults.public_url}.`}
              >
                <TextInput
                  id="public_url"
                  className="font-mono text-[12.5px]"
                  placeholder={settings.defaults.public_url}
                  value={draft.public_url}
                  onChange={(event) => set('public_url', event.target.value)}
                />
              </Field>
              <Effective label="In use" value={settings.effective.public_url} />
            </Panel>

            <Panel
              eyebrow="Agent"
              title="What gets installed into a cluster"
              bodyClassName="flex flex-col gap-4 p-4"
            >
              <Field
                label="Agent image"
                htmlFor="agent_image"
                hint={`Container image the generated manifests install. Leave empty for ${settings.defaults.agent_image}.`}
              >
                <TextInput
                  id="agent_image"
                  className="font-mono text-[12.5px]"
                  placeholder={settings.defaults.agent_image}
                  value={draft.agent_image}
                  onChange={(event) => set('agent_image', event.target.value)}
                />
              </Field>
              <Effective label="In use" value={settings.effective.agent_image} />

              <Field
                label="Agent namespace"
                htmlFor="agent_namespace"
                hint={`Namespace the agent is installed into on target clusters. Leave empty for ${settings.defaults.agent_namespace}.`}
              >
                <TextInput
                  id="agent_namespace"
                  className="font-mono text-[12.5px]"
                  placeholder={settings.defaults.agent_namespace}
                  value={draft.agent_namespace}
                  onChange={(event) => set('agent_namespace', event.target.value)}
                />
              </Field>
              <Effective label="In use" value={settings.effective.agent_namespace} />
            </Panel>

            {/* What the trail keeps, for how long, and whether shells are
                recorded. Three panels rather than one, because they are three
                decisions with different consequences. */}
            <AuditSettingsPanel
              settings={settings}
              selectedVerbs={draft.audit_verbs}
              onVerbsChange={(next) => set('audit_verbs', next)}
              recordSessions={draft.record_exec_sessions}
              onRecordSessionsChange={(next) => set('record_exec_sessions', next)}
              retentionDays={draft.audit_retention_days}
              onRetentionChange={(next) => set('audit_retention_days', next)}
              recordingRetentionDays={draft.session_recording_retention_days}
              onRecordingRetentionChange={(next) =>
                set('session_recording_retention_days', next)
              }
              retentionError={
                retentionValid ? undefined : 'Retention must be a whole number of days, up to 3650.'
              }
              recordingRetentionError={
                recordingRetentionValid
                  ? undefined
                  : 'Retention must be a whole number of days, up to 3650.'
              }
            />

            {/* What the platform refuses to pass on, whatever the cluster's
                RBAC allows. It sits next to the audit panels because it is the
                same subject read the other way round: the trail says what
                happened, a guardrail is what does not get to. Like them it owns
                its own saving — a rule is edited in a sheet. */}
            <GuardrailSettingsPanel clusters={clusters} />

            {/* Where a cluster event or a refused action goes. Like the SSO
                panel it owns its own saving — a channel is edited in a sheet and
                saved on its own, not by the page's Save button. */}
            <AlarmSettingsPanel clusters={clusters} />

            {/* Who may sign in at all. It sits inside the settings form's flow
                but owns its own saving — a provider is edited in a sheet and
                saved on its own, not by the page's Save button. */}
            <SsoSettingsPanel />

            {/* Settings only reach clusters registered from here on: an agent
                already running holds the address it was installed with. */}
            <Notice tone="info">
              A change applies to install commands generated from now on. Agents already running keep
              the address they were installed with — re-apply their manifest from the cluster page to
              move them.
            </Notice>
          </>
        ) : null}
      </form>
    </AppShell>
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
