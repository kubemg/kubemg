import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../api/client'
import type { RuntimeSettings, SettingsResponse } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, Field, Notice, Panel, TextInput } from '../components/primitives'
import { SsoSettingsPanel } from '../components/SsoSettingsPanel'

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
}

const EMPTY: Draft = {
  public_url: '',
  agent_image: '',
  agent_namespace: '',
  audit_retention_days: '',
}

const KEYS = ['public_url', 'agent_image', 'agent_namespace'] as const

/** draftOf turns a settings response into the form's own shape. */
function draftOf(overrides: RuntimeSettings): Draft {
  return {
    public_url: overrides.public_url,
    agent_image: overrides.agent_image,
    agent_namespace: overrides.agent_namespace,
    // 0 is how the API says "unset", which the form shows as an empty box.
    audit_retention_days:
      overrides.audit_retention_days > 0 ? String(overrides.audit_retention_days) : '',
  }
}

export function Settings() {
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
      setDraft(draftOf(next.overrides))
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
      })
      setSettings(next)
      setDraft(draftOf(next.overrides))
      setSaved(true)
    } catch (err) {
      setError(errorMessage(err, 'Could not save the settings.'))
    } finally {
      setBusy(false)
    }
  }

  function set(key: keyof Draft, value: string) {
    setDraft((current) => ({ ...current, [key]: value }))
    setSaved(false)
  }

  const retentionDays = Number(draft.audit_retention_days.trim() || 0)
  const retentionValid =
    Number.isInteger(retentionDays) && retentionDays >= 0 && retentionDays <= 3650

  const dirty =
    settings !== null &&
    (KEYS.some((key) => draft[key].trim() !== settings.overrides[key]) ||
      retentionDays !== settings.overrides.audit_retention_days)

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
                setDraft(draftOf(settings.overrides))
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
              disabled={busy || !dirty || !retentionValid}
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

            <Panel
              eyebrow="Audit"
              title="How long the trail is kept"
              description="Every proxied call is recorded, including refusals and both ends of a streamed session, so the table grows with fleet activity rather than with fleet size. A background pass prunes anything past this window twice a day."
              bodyClassName="flex flex-col gap-4 p-4"
            >
              <Field
                label="Retention (days)"
                htmlFor="audit_retention_days"
                hint={`Records older than this are deleted and cannot be recovered. Leave empty for ${settings.defaults.audit_retention_days} days.`}
                error={
                  retentionValid ? undefined : 'Retention must be a whole number of days, up to 3650.'
                }
              >
                <TextInput
                  id="audit_retention_days"
                  type="number"
                  min={1}
                  max={3650}
                  step={1}
                  inputMode="numeric"
                  className="max-w-40 font-mono text-[12.5px]"
                  placeholder={String(settings.defaults.audit_retention_days)}
                  value={draft.audit_retention_days}
                  onChange={(event) => set('audit_retention_days', event.target.value)}
                />
              </Field>
              <Effective label="In use" value={`${settings.effective.audit_retention_days} days`} />
            </Panel>

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
