import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../../api/client'
import type { SettingsResponse } from '../../api/types'
import { Button, Field, Notice, Panel, TextInput } from '../../components/primitives'
import { settingSource } from '../../lib/settings'
import { SettingsAside, SettingsLayout } from '../../components/settings/SettingsLayout'

/** The ceiling the build refuses to go past, whatever is typed here. It matches
    k8s.MaxTTL on the server, which enforces it — this copy only keeps the form
    from offering a value that would come back refused. */
const MAX_CEILING_HOURS = 90 * 24

/** GeneralSettings owns two server-wide decisions: where a target cluster
    reaches this KubeMG, and how long a credential it hands out may live. */
export function GeneralSettings() {
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [publicUrl, setPublicUrl] = useState('')
  // Blank means "use the default", so the form state is the override rather
  // than the value — the same reason audit retention is edited as a string.
  const [ceiling, setCeiling] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const load = useCallback(async () => {
    try {
      const next = await fetchSettings()
      setSettings(next)
      setPublicUrl(next.overrides.public_url)
      setCeiling(ceilingDraft(next))
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
        public_url: publicUrl.trim(),
        // 0 is how the API says "clear it", which the form shows as an empty box.
        kubeconfig_max_ttl_hours: Number(ceiling.trim()) || 0,
      })
      setSettings(next)
      setPublicUrl(next.overrides.public_url)
      setCeiling(ceilingDraft(next))
      setSaved(true)
    } catch (err) {
      setError(errorMessage(err, 'Could not save the settings.'))
    } finally {
      setBusy(false)
    }
  }

  // The server enforces the same bounds; catching it here saves a round trip
  // whose only answer is the number already in the hint.
  const ceilingError =
    ceiling.trim() === '' || withinCeilingBounds(ceiling)
      ? undefined
      : `Enter a whole number of hours between 1 and ${MAX_CEILING_HOURS}, or leave it empty for the default.`

  const dirty =
    settings !== null &&
    (publicUrl.trim() !== settings.overrides.public_url || ceiling.trim() !== ceilingDraft(settings))

  return (
    <SettingsLayout
      title="General settings"
      aside={
        settings ? (
          <>
            <SettingsAside
              label="Server URL in force"
              value={settings.effective.public_url}
              source={settingSource(settings.overrides.public_url, settings.defaults.public_url)}
              reach="Every agent install command rendered from now on, and the address in every kubeconfig issued for an agent-mode cluster. An agent already running keeps the address it was installed with until its manifests are re-applied."
            />
            <SettingsAside
              label="Longest kubeconfig window"
              value={humanHours(settings.effective.kubeconfig_max_ttl_hours)}
              source={settingSource(settings.overrides.kubeconfig_max_ttl_hours, 0)}
              reach="Credentials issued from now on. A kubeconfig already in somebody's hands keeps the window it was signed with — shortening the ceiling does not shorten it, revoking it does."
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
                setPublicUrl(settings.overrides.public_url)
                setCeiling(ceilingDraft(settings))
                setSaved(false)
              }}
            >
              <RotateCcw aria-hidden="true" className="size-4" />
              Discard
            </Button>
            <Button
              type="submit"
              form="general-settings-form"
              variant="primary"
              disabled={busy || !dirty || ceilingError !== undefined}
            >
              {busy ? 'Saving…' : 'Save settings'}
            </Button>
          </>
        ) : null
      }
    >
      <form
        id="general-settings-form"
        onSubmit={save}
        className="flex min-w-0 flex-col gap-4"
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
        {settings?.warnings.map((warning) => (
          <Notice key={warning} tone="warn">
            {warning}
          </Notice>
        ))}
        {saved && !dirty ? <Notice tone="ok">Saved.</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {settings ? (
          <Panel
            eyebrow="Server"
            title="Where clusters reach kubemg"
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
                value={publicUrl}
                onChange={(event) => {
                  setPublicUrl(event.target.value)
                  setSaved(false)
                }}
              />
            </Field>
          </Panel>
        ) : null}

        {settings ? (
          <Panel
            eyebrow="Cluster access"
            title="How long a kubeconfig may live"
            description="The ceiling anyone generating a kubeconfig is measured against. They still choose their own window inside it — this is the longest one the choice offers."
            bodyClassName="flex flex-col gap-4 p-4"
          >
            <Field
              label="Longest window (hours)"
              htmlFor="kubeconfig_max_ttl_hours"
              hint={`Leave empty for the default of ${settings.defaults.kubeconfig_max_ttl_hours} hours. 2160 is three months, 720 a month, 8 a shift.`}
              error={ceilingError}
            >
              <TextInput
                id="kubeconfig_max_ttl_hours"
                type="number"
                min={1}
                max={MAX_CEILING_HOURS}
                step={1}
                inputMode="numeric"
                className="max-w-40 font-mono text-[12.5px]"
                placeholder={String(settings.defaults.kubeconfig_max_ttl_hours)}
                value={ceiling}
                onChange={(event) => {
                  setCeiling(event.target.value)
                  setSaved(false)
                }}
              />
            </Field>
          </Panel>
        ) : null}
      </form>
    </SettingsLayout>
  )
}

function withinCeilingBounds(raw: string): boolean {
  const hours = Number(raw)
  return Number.isInteger(hours) && hours >= 1 && hours <= MAX_CEILING_HOURS
}

/** ceilingDraft is the stored override as the form shows it: 0 from the API
    means unset, which is an empty box rather than a zero. */
function ceilingDraft(settings: SettingsResponse): string {
  const hours = settings.overrides.kubeconfig_max_ttl_hours
  return hours > 0 ? String(hours) : ''
}

/** humanHours says a window the way an operator set it — "90 days" rather than
    "2160 hours", which is not how anyone says a quarter. */
function humanHours(hours: number): string {
  if (hours <= 0) return 'the default'
  if (hours % 24 === 0) {
    const days = hours / 24
    return days === 1 ? '1 day' : `${days} days`
  }
  return hours === 1 ? '1 hour' : `${hours} hours`
}
