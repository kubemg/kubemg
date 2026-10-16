import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import { errorMessage, fetchSettings, updateSettings } from '../../api/client'
import type { KubeconfigIssuance, MFAEnforcement, SettingsResponse } from '../../api/types'
import { Button, Field, Notice, Panel, Select, TextInput } from '../../components/primitives'
import { settingSource } from '../../lib/settings'
import { SettingsAside, SettingsLayout } from '../../components/settings/SettingsLayout'
import { useUnsavedGuard } from '../../lib/unsavedGuard'

/** The ceiling the build refuses to go past, whatever is typed here. It matches
    k8s.MaxTTL on the server, which enforces it — this copy only keeps the form
    from offering a value that would come back refused. */
const MAX_CEILING_HOURS = 90 * 24

/** The words for each enforcement policy, in the order the select offers them. */
const MFA_POLICY: { value: MFAEnforcement; label: string; reach: string }[] = [
  { value: 'off', label: 'Optional', reach: 'Anyone may set up a second factor; nobody is made to.' },
  {
    value: 'admins',
    label: 'Administrators',
    reach: 'Administrators and super admins set one up at their next sign-in and cannot remove their last.',
  },
  {
    value: 'all',
    label: 'Everyone',
    reach: 'Every local and LDAP account sets one up at its next sign-in and cannot remove its last.',
  },
]

/** GeneralSettings owns three server-wide decisions: where a target cluster
    reaches this KubeMG, how long a credential it hands out may live, and whose
    password sign-in also needs a second factor. */
export function GeneralSettings() {
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [publicUrl, setPublicUrl] = useState('')
  // Blank means "use the default", so the form state is the override rather
  // than the value — the same reason audit retention is edited as a string.
  const [ceiling, setCeiling] = useState('')
  const [mfaPolicy, setMfaPolicy] = useState<MFAEnforcement>('off')
  const [cliHours, setCliHours] = useState('')
  const [issuance, setIssuance] = useState<KubeconfigIssuance>('allowed')
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
      setMfaPolicy(next.effective.mfa_enforcement || 'off')
      setCliHours(cliHoursDraft(next))
      setIssuance(next.effective.kubeconfig_issuance || 'allowed')
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
    // The button stays live; a value that cannot be saved is pointed at here,
    // where its error is already written under the field.
    if (ceilingError !== undefined) {
      document.getElementById('kubeconfig_max_ttl_hours')?.focus()
      return
    }
    if (cliHoursError !== undefined) {
      document.getElementById('cli_session_max_hours')?.focus()
      return
    }
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      const next = await updateSettings({
        public_url: publicUrl.trim(),
        // 0 is how the API says "clear it", which the form shows as an empty box.
        kubeconfig_max_ttl_hours: Number(ceiling.trim()) || 0,
        // Off is stored as the cleared value, so the row says "unset" rather
        // than recording a decision nobody made.
        mfa_enforcement: mfaPolicy === 'off' ? '' : mfaPolicy,
        cli_session_max_hours: Number(cliHours.trim()) || 0,
        kubeconfig_issuance: issuance === 'allowed' ? '' : issuance,
      })
      setSettings(next)
      setPublicUrl(next.overrides.public_url)
      setCeiling(ceilingDraft(next))
      setMfaPolicy(next.effective.mfa_enforcement || 'off')
      setCliHours(cliHoursDraft(next))
      setIssuance(next.effective.kubeconfig_issuance || 'allowed')
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

  const cliHoursError =
    cliHours.trim() === '' || withinBounds(cliHours, 1, 720)
      ? undefined
      : 'Enter a whole number of hours between 1 and 720, or leave it empty for the default.'

  const dirty =
    settings !== null &&
    (publicUrl.trim() !== settings.overrides.public_url ||
      ceiling.trim() !== ceilingDraft(settings) ||
      mfaPolicy !== (settings.effective.mfa_enforcement || 'off') ||
      cliHours.trim() !== cliHoursDraft(settings) ||
      issuance !== (settings.effective.kubeconfig_issuance || 'allowed'))
  useUnsavedGuard(dirty, 'these settings')

  return (
    <SettingsLayout
      title="General settings"
      description="Where clusters reach this server, how long a kubeconfig may live, and who needs a second factor to sign in. A value set here overrides what the server read from its environment at boot; clearing it falls back to that."
      aside={
        settings ? (
          <>
            <SettingsAside
              label="Server URL in force"
              value={settings.effective.public_url}
              source={settingSource(settings.overrides.public_url, settings.defaults.public_url)}
              reach="Every agent install command rendered from now on, and the address in every kubeconfig issued for an agent-mode cluster. An agent already running keeps the address it was installed with until its manifests are re-applied. Security keys are bound to its host: changing the host retires every key registered under the old one, and their owners sign in with another factor or a recovery code and register again."
            />
            <SettingsAside
              label="Longest kubeconfig window"
              value={humanHours(settings.effective.kubeconfig_max_ttl_hours)}
              source={settingSource(settings.overrides.kubeconfig_max_ttl_hours, 0)}
              reach="Credentials issued from now on. A kubeconfig already in somebody's hands keeps the window it was signed with — shortening the ceiling does not shorten it, revoking it does."
            />
            <SettingsAside
              label="Second factor"
              value={MFA_POLICY.find((p) => p.value === (settings.effective.mfa_enforcement || 'off'))?.label ?? 'Optional'}
              source={settingSource(settings.overrides.mfa_enforcement, '')}
              reach="Sign-ins from now on. A session already open runs to its own expiry — requiring a factor does not end it."
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
                setMfaPolicy(settings.effective.mfa_enforcement || 'off')
                setCliHours(cliHoursDraft(settings))
                setIssuance(settings.effective.kubeconfig_issuance || 'allowed')
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
              disabled={busy || !dirty}
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
                className="font-data text-[12.5px]"
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
                className="max-w-40 font-data text-[12.5px]"
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

        {settings ? (
          <Panel
            eyebrow="Cluster access"
            title="The kubemg command line"
            description="kubemg login signs a command line in through this console — your second factor included — and it then hands kubectl a fifteen-minute credential at a time."
            bodyClassName="flex flex-col gap-4 p-4"
          >
            <Field
              label="A sign-in lasts (hours)"
              htmlFor="cli_session_max_hours"
              hint={`Leave empty for the default of ${settings.defaults.cli_session_max_hours} hours. Never longer than the kubeconfig window above.`}
              error={cliHoursError}
            >
              <TextInput
                id="cli_session_max_hours"
                type="number"
                min={1}
                max={720}
                step={1}
                inputMode="numeric"
                className="max-w-40 font-data text-[12.5px]"
                placeholder={String(settings.defaults.cli_session_max_hours)}
                value={cliHours}
                onChange={(event) => {
                  setCliHours(event.target.value)
                  setSaved(false)
                }}
              />
            </Field>
            <Field
              label="Agent-mode kubeconfig files"
              htmlFor="kubeconfig_issuance"
              hint={
                issuance === 'cli_only'
                  ? 'Not handed out. kubectl reaches agent-mode clusters through the CLI only. Direct-mode clusters are unaffected — their credential is a token the cluster mints, which no CLI session governs.'
                  : 'Handed out from the console, alongside the CLI.'
              }
            >
              <Select
                id="kubeconfig_issuance"
                className="max-w-60"
                value={issuance}
                onChange={(event) => {
                  setIssuance(event.target.value as KubeconfigIssuance)
                  setSaved(false)
                }}
              >
                <option value="allowed">Allowed</option>
                <option value="cli_only">CLI only</option>
              </Select>
            </Field>
          </Panel>
        ) : null}

        {settings ? (
          <Panel
            eyebrow="Sign-in"
            title="Second factor"
            description="Whose password sign-in also asks for a code from an authenticator app. It covers local and LDAP accounts; an OIDC or SAML account's second factor is its identity provider's."
            bodyClassName="flex flex-col gap-4 p-4"
          >
            <Field
              label="Required for"
              htmlFor="mfa_enforcement"
              hint={MFA_POLICY.find((p) => p.value === mfaPolicy)?.reach}
            >
              <Select
                id="mfa_enforcement"
                className="max-w-60"
                value={mfaPolicy}
                onChange={(event) => {
                  setMfaPolicy(event.target.value as MFAEnforcement)
                  setSaved(false)
                }}
              >
                {MFA_POLICY.map((policy) => (
                  <option key={policy.value} value={policy.value}>
                    {policy.label}
                  </option>
                ))}
              </Select>
            </Field>
          </Panel>
        ) : null}
      </form>
    </SettingsLayout>
  )
}

function withinCeilingBounds(raw: string): boolean {
  return withinBounds(raw, 1, MAX_CEILING_HOURS)
}

function withinBounds(raw: string, min: number, max: number): boolean {
  const hours = Number(raw)
  return Number.isInteger(hours) && hours >= min && hours <= max
}

/** The stored override as the form shows it: 0 from the API is an empty box. */
function cliHoursDraft(settings: SettingsResponse): string {
  const hours = settings.overrides.cli_session_max_hours
  return hours > 0 ? String(hours) : ''
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
