import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../../api/client'
import type { SettingsResponse } from '../../api/types'
import { Button, Field, Notice, Panel, TextInput } from '../../components/primitives'
import { settingSource } from '../../lib/settings'
import { SettingsAside, SettingsLayout } from '../../components/settings/SettingsLayout'

type Draft = {
  agent_image: string
  agent_namespace: string
  shell_enabled: boolean
  shell_image: string
  shell_idle_timeout_minutes: string
  shell_max_lifetime_hours: string
}

function draftOf(settings: SettingsResponse): Draft {
  return {
    agent_image: settings.overrides.agent_image,
    agent_namespace: settings.overrides.agent_namespace,
    // The switch has no "unset": what the server reports as effective is what
    // the box is showing, and a server with no image reports it off.
    shell_enabled: settings.effective.shell_enabled,
    shell_image: settings.overrides.shell_image,
    shell_idle_timeout_minutes: numberField(settings.overrides.shell_idle_timeout_minutes),
    shell_max_lifetime_hours: numberField(settings.overrides.shell_max_lifetime_hours),
  }
}

/** An unset numeric override is 0 on the wire and an empty box on screen: the
    field's placeholder is what says which default is in force. */
function numberField(value: number): string {
  return value > 0 ? String(value) : ''
}

/** AgentSettings owns what gets installed into a cluster: the image and the
    namespace every generated manifest carries. */
export function AgentSettings() {
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [draft, setDraft] = useState<Draft>({
    agent_image: '',
    agent_namespace: '',
    shell_enabled: false,
    shell_image: '',
    shell_idle_timeout_minutes: '',
    shell_max_lifetime_hours: '',
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

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      const next = await updateSettings({
        agent_image: draft.agent_image.trim(),
        agent_namespace: draft.agent_namespace.trim(),
        shell_enabled: draft.shell_enabled,
        shell_image: draft.shell_image.trim(),
        // 0 clears an override back to the build's default, the rule every
        // numeric setting here follows.
        shell_idle_timeout_minutes: Number(draft.shell_idle_timeout_minutes.trim()) || 0,
        shell_max_lifetime_hours: Number(draft.shell_max_lifetime_hours.trim()) || 0,
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

  function set(key: keyof Draft, value: string | boolean) {
    setDraft((current) => ({ ...current, [key]: value }))
    setSaved(false)
  }

  const dirty =
    settings !== null &&
    (draft.agent_image.trim() !== settings.overrides.agent_image ||
      draft.agent_namespace.trim() !== settings.overrides.agent_namespace ||
      draft.shell_enabled !== settings.effective.shell_enabled ||
      draft.shell_image.trim() !== settings.overrides.shell_image ||
      draft.shell_idle_timeout_minutes.trim() !==
        numberField(settings.overrides.shell_idle_timeout_minutes) ||
      draft.shell_max_lifetime_hours.trim() !==
        numberField(settings.overrides.shell_max_lifetime_hours))

  return (
    <SettingsLayout
      title="Agent settings"
      aside={
        settings ? (
          <>
            <SettingsAside
              label="Agent image in force"
              value={settings.effective.agent_image}
              source={settingSource(settings.overrides.agent_image, settings.defaults.agent_image)}
              reach="Install packages rendered from now on. An agent already running keeps the image it was installed with until somebody re-applies its manifests — changing this upgrades nothing on its own."
            />
            <SettingsAside
              label="Agent namespace"
              value={settings.effective.agent_namespace}
              source={settingSource(settings.overrides.agent_namespace, settings.defaults.agent_namespace)}
              reach="New installs only. An agent already running lives where it was installed, and the shell runner's Role is bound in that namespace."
            />
            <SettingsAside
              label="Browser shell"
              value={settings.effective.shell_enabled ? settings.effective.shell_image || 'on' : 'off'}
              reach={
                settings.defaults.shell_enabled
                  ? 'Every agent-mode cluster, on the next shell somebody opens. Turning it off refuses new shells and leaves the ones already open running — a session somebody is mid-command in is not a setting.'
                  : 'This server was started without a shell image, so a stored value can only ever keep it off.'
              }
            />
          </>
        ) : null
      }
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
            <Button type="submit" form="agent-settings-form" variant="primary" disabled={busy || !dirty}>
              {busy ? 'Saving…' : 'Save settings'}
            </Button>
          </>
        ) : null
      }
    >
      <form id="agent-settings-form" onSubmit={save} className="flex min-w-0 flex-col gap-4">
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
            </Panel>

            {/* The browser shell lives on this page rather than beside the
                audit switches: it is a second thing KubeMG runs on somebody
                else's cluster, and the questions it raises — which image, how
                long it lives — are the agent's questions. */}
            <Panel
              eyebrow="Browser shell"
              title="A terminal KubeMG runs on a cluster"
              bodyClassName="flex flex-col gap-4 p-4"
            >
              <label className="flex items-start gap-2.5">
                <input
                  type="checkbox"
                  className="mt-0.5 size-4 accent-[var(--color-accent)]"
                  checked={draft.shell_enabled}
                  disabled={!settings.defaults.shell_enabled}
                  onChange={(event) => set('shell_enabled', event.target.checked)}
                />
                <span className="min-w-0">
                  <span className="text-[13px] text-fg">Offer a browser shell on agent-mode clusters</span>
                  <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                    A pod with <code>kubectl</code> and <code>helm</code> in it, started only when
                    somebody asks. It holds no cluster credential of its own: its kubeconfig points
                    back at this server, so every command it runs is impersonated as the operator,
                    answered by the cluster's own RBAC and audited. Turning this off refuses new
                    shells and leaves running ones alone.
                    {settings.defaults.shell_enabled
                      ? ''
                      : ' This server was started without a shell image, so there is nothing to run.'}
                  </span>
                </span>
              </label>

              <Field
                label="Shell image"
                htmlFor="shell_image"
                hint={`Leave empty for ${settings.defaults.shell_image || 'the build’s own image'}. An air-gapped site points this at its mirror.`}
              >
                <TextInput
                  id="shell_image"
                  className="font-mono text-[12.5px]"
                  placeholder={settings.defaults.shell_image}
                  value={draft.shell_image}
                  onChange={(event) => set('shell_image', event.target.value)}
                />
              </Field>

              <Field
                label="Idle timeout (minutes)"
                htmlFor="shell_idle_timeout_minutes"
                hint={`How long a shell may go without a keystroke before it is reclaimed. Leave empty for ${settings.defaults.shell_idle_timeout_minutes} minutes.`}
              >
                <TextInput
                  id="shell_idle_timeout_minutes"
                  inputMode="numeric"
                  placeholder={String(settings.defaults.shell_idle_timeout_minutes)}
                  value={draft.shell_idle_timeout_minutes}
                  onChange={(event) => set('shell_idle_timeout_minutes', event.target.value)}
                />
              </Field>

              <Field
                label="Maximum lifetime (hours)"
                htmlFor="shell_max_lifetime_hours"
                hint={`Written into the pod itself, so it holds even while this server is down. Capped by the kubeconfig ceiling — a shell must not outlive the credential inside it. Leave empty for ${settings.defaults.shell_max_lifetime_hours} hours.`}
              >
                <TextInput
                  id="shell_max_lifetime_hours"
                  inputMode="numeric"
                  placeholder={String(settings.defaults.shell_max_lifetime_hours)}
                  value={draft.shell_max_lifetime_hours}
                  onChange={(event) => set('shell_max_lifetime_hours', event.target.value)}
                />
              </Field>
            </Panel>

            <Notice tone="info">
              A change applies to install commands generated from now on. Agents already running keep
              the address they were installed with — re-apply their manifest from the cluster page to
              move them.
            </Notice>
          </>
        ) : null}
      </form>
    </SettingsLayout>
  )
}
