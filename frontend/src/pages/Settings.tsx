import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../api/client'
import type { SettingsResponse } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, Field, Notice, Panel, TextInput } from '../components/primitives'

/** Blank means "use the default", so the form state is the override, not the value. */
type Draft = { public_url: string; agent_image: string; agent_namespace: string }

const EMPTY: Draft = { public_url: '', agent_image: '', agent_namespace: '' }

const KEYS = ['public_url', 'agent_image', 'agent_namespace'] as const

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
      setDraft({ ...next.overrides })
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
      })
      setSettings(next)
      setDraft({ ...next.overrides })
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

  const dirty =
    settings !== null && KEYS.some((key) => draft[key].trim() !== settings.overrides[key])

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
                setDraft({ ...settings.overrides })
                setSaved(false)
              }}
            >
              <RotateCcw aria-hidden="true" className="size-4" />
              Discard
            </Button>
            <Button type="submit" form="settings-form" variant="primary" disabled={busy || !dirty}>
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
        {saved && !dirty ? <Notice tone="ok">Saved. New install commands use it.</Notice> : null}
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
