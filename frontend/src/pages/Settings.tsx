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
    settings !== null &&
    (['public_url', 'agent_image', 'agent_namespace'] as const).some(
      (key) => draft[key].trim() !== settings.overrides[key],
    )

  return (
    <AppShell title="Settings">
      <form onSubmit={save} className="flex min-w-0 max-w-3xl flex-col gap-3">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {settings?.warnings.map((warning) => (
          <Notice key={warning} tone="warn">
            {warning}
          </Notice>
        ))}
        {saved && !dirty ? <Notice tone="info">Saved. New install commands use it.</Notice> : null}
        {loading ? <p className="text-[12px] text-muted">Loading…</p> : null}

        {settings ? (
          <>
            <Panel title="Server">
              <div className="flex flex-col gap-3 p-3.5">
                <Field
                  label="Server URL"
                  htmlFor="public_url"
                  hint={`The address a target cluster reaches KubeMG on. It is baked into every agent install command and into the agent's tunnel address, so it must be routable from inside the cluster — not the address your browser uses. Leave empty for ${settings.defaults.public_url}.`}
                >
                  <TextInput
                    id="public_url"
                    className="font-mono text-[12px]"
                    placeholder={settings.defaults.public_url}
                    value={draft.public_url}
                    onChange={(event) => set('public_url', event.target.value)}
                  />
                </Field>
                <Effective label="In use" value={settings.effective.public_url} />
              </div>
            </Panel>

            <Panel title="Agent">
              <div className="flex flex-col gap-3 p-3.5">
                <Field
                  label="Agent image"
                  htmlFor="agent_image"
                  hint={`Container image the generated manifests install. Leave empty for ${settings.defaults.agent_image}.`}
                >
                  <TextInput
                    id="agent_image"
                    className="font-mono text-[12px]"
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
                    className="font-mono text-[12px]"
                    placeholder={settings.defaults.agent_namespace}
                    value={draft.agent_namespace}
                    onChange={(event) => set('agent_namespace', event.target.value)}
                  />
                </Field>
                <Effective label="In use" value={settings.effective.agent_namespace} />
              </div>
            </Panel>

            {/* Settings only reach clusters registered from here on: an agent
                already running holds the address it was installed with. */}
            <Notice tone="info">
              A change applies to install commands generated from now on. Agents already running keep
              the address they were installed with — re-apply their manifest from the cluster page to
              move them.
            </Notice>

            <div className="flex items-center gap-2">
              <Button type="submit" variant="primary" disabled={busy || !dirty}>
                {busy ? 'Saving…' : 'Save settings'}
              </Button>
              <Button
                type="button"
                variant="ghost"
                disabled={busy || !dirty}
                onClick={() => {
                  setDraft({ ...settings.overrides })
                  setSaved(false)
                }}
              >
                <RotateCcw aria-hidden="true" className="size-3.5" />
                Discard
              </Button>
            </div>
          </>
        ) : null}
      </form>
    </AppShell>
  )
}

function Effective({ label, value }: { label: string; value: string }) {
  return (
    <p className="flex flex-wrap items-baseline gap-2 border-t border-line-soft pt-2">
      <span className="label">{label}</span>
      <span className="font-mono text-[12px] text-fg">{value}</span>
    </p>
  )
}
