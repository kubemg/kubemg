import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../../api/client'
import type { SettingsResponse } from '../../api/types'
import { Button, Field, Notice, Panel, TextInput } from '../../components/primitives'
import { SettingsLayout } from '../../components/settings/SettingsLayout'

/** GeneralSettings owns the one override every install command is rendered
    from: where a target cluster reaches this KubeMG. */
export function GeneralSettings() {
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [publicUrl, setPublicUrl] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const load = useCallback(async () => {
    try {
      const next = await fetchSettings()
      setSettings(next)
      setPublicUrl(next.overrides.public_url)
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
      const next = await updateSettings({ public_url: publicUrl.trim() })
      setSettings(next)
      setPublicUrl(next.overrides.public_url)
      setSaved(true)
    } catch (err) {
      setError(errorMessage(err, 'Could not save the settings.'))
    } finally {
      setBusy(false)
    }
  }

  const dirty = settings !== null && publicUrl.trim() !== settings.overrides.public_url

  return (
    <SettingsLayout
      title="General settings"
      actions={
        settings ? (
          <>
            <Button
              type="button"
              variant="ghost"
              disabled={busy || !dirty}
              onClick={() => {
                setPublicUrl(settings.overrides.public_url)
                setSaved(false)
              }}
            >
              <RotateCcw aria-hidden="true" className="size-4" />
              Discard
            </Button>
            <Button type="submit" form="general-settings-form" variant="primary" disabled={busy || !dirty}>
              {busy ? 'Saving…' : 'Save settings'}
            </Button>
          </>
        ) : null
      }
    >
      <form
        id="general-settings-form"
        onSubmit={save}
        className="flex min-w-0 max-w-3xl flex-col gap-4"
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
                value={publicUrl}
                onChange={(event) => {
                  setPublicUrl(event.target.value)
                  setSaved(false)
                }}
              />
            </Field>
            <Effective label="In use" value={settings.effective.public_url} />
          </Panel>
        ) : null}
      </form>
    </SettingsLayout>
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
