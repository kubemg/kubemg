import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate } from 'react-router'
import { ArrowLeft, ArrowRight } from 'lucide-react'
import {
  completeSetup,
  errorMessage,
  fetchSettings,
  fetchSetupPreflight,
  updateSettings,
  updateUser,
} from '../api/client'
import type { SettingsResponse, SetupPreflight } from '../api/types'
import { Lockup } from '../components/Mark'
import { SsoSettingsPanel } from '../components/SsoSettingsPanel'
import { AuditSettingsPanel } from '../components/settings/AuditSettingsPanel'
import { DeploymentCheckList } from '../components/settings/DeploymentChecks'
import { Button, Field, Notice, Panel, Spinner, TextInput } from '../components/primitives'
import { StepActions, Stepper } from '../components/WizardChrome'
import { useAuth } from '../state/auth-context'

/*
 * First-run setup.
 *
 * The premise is that a fleet gateway should be installable without writing a
 * configuration file first. `docker compose up -d` brings the management plane
 * up with nothing set, and this is what turns that into a configured bastion:
 * the administrator's password, the address clusters dial, where the agent image
 * comes from, what the trail keeps, and optionally who may sign in — in the
 * order they matter, each one saved as you leave it.
 *
 * Two things it deliberately does not do.
 *
 * It has no write surface of its own. Every field here saves through the same
 * endpoint the corresponding Settings page uses, with the same validation. A
 * second path to the same values is a second path that can drift from the first,
 * and the one that drifts is always the one nobody looks at afterwards.
 *
 * And it does not pretend to own what it cannot. The database credentials, the
 * recording encryption key and the TLS certificate files are read once, at boot,
 * from an environment this process cannot rewrite — a form collecting them would
 * be collecting values that vanish at the next restart. The Preflight step
 * reports them instead, with the line to set and where, before the operator
 * leaves rather than after.
 *
 * It runs exactly once. Finishing stamps the install and every field here goes
 * back to living on its own Settings page.
 */

const STEPS = [
  'Administrator',
  'Address',
  'Images',
  'Trail',
  'Sign-in',
  'Preflight',
] as const

/** Matching the server's minimum, so the box says no before the request does. */
const MIN_PASSWORD_LENGTH = 8

export function Setup() {
  const navigate = useNavigate()
  const { user, refreshSetupState } = useAuth()

  const [step, setStep] = useState(0)
  const [settings, setSettings] = useState<SettingsResponse | null>(null)
  const [preflight, setPreflight] = useState<SetupPreflight | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // The furthest step reached. Setup is a sequence, but it is a sequence of
  // saves rather than of dependencies — nothing later needs anything earlier —
  // so once a step has been visited it stays reachable and an operator can go
  // back and change their mind.
  const [furthest, setFurthest] = useState(0)

  const load = useCallback(async () => {
    try {
      const [nextSettings, nextPreflight] = await Promise.all([
        fetchSettings(),
        fetchSetupPreflight(),
      ])
      setSettings(nextSettings)
      setPreflight(nextPreflight)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read this server’s configuration.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const go = useCallback((next: number) => {
    setStep(next)
    setFurthest((current) => Math.max(current, next))
  }, [])

  async function finish() {
    await completeSetup()
    await refreshSetupState()
    navigate('/admin/clusters/new', { replace: true })
  }

  return (
    <div className="min-h-svh bg-bg">
      {/* No AppShell: there is no fleet to navigate yet, and a sidebar offering
          pages that have not been configured would be inviting the operator to
          leave halfway through. */}
      <header className="border-b border-line-soft">
        <div className="mx-auto flex max-w-4xl flex-wrap items-baseline gap-x-3 gap-y-1 px-5 py-5">
          <Lockup className="text-[18px] text-fg" />
          <h1 className="text-[15px] font-medium text-fg">Set up this bastion</h1>
          <p className="label ml-auto">signed in as {user?.username}</p>
        </div>
      </header>

      <main className="mx-auto flex max-w-4xl min-w-0 flex-col gap-5 px-5 py-6">
        <Stepper steps={STEPS} current={step} furthest={furthest} onSelect={go} />

        {error ? <Notice tone="error">{error}</Notice> : null}

        {loading ? (
          <p className="flex items-center gap-2 text-[13px] text-muted">
            <Spinner className="size-4" />
            Reading this server’s configuration…
          </p>
        ) : null}

        {settings ? (
          <>
            {step === 0 ? (
              <AdministratorStep
                pristine={preflight?.admin_password_pristine ?? false}
                onSaved={() => void load()}
                onNext={() => go(1)}
              />
            ) : null}

            {step === 1 ? (
              <AddressStep
                settings={settings}
                onSettings={setSettings}
                onBack={() => go(0)}
                onNext={() => go(2)}
              />
            ) : null}

            {step === 2 ? (
              <ImagesStep
                settings={settings}
                onSettings={setSettings}
                onBack={() => go(1)}
                onNext={() => go(3)}
              />
            ) : null}

            {step === 3 ? (
              <TrailStep
                settings={settings}
                onSettings={setSettings}
                onBack={() => go(2)}
                onNext={() => go(4)}
              />
            ) : null}

            {step === 4 ? <SignInStep onBack={() => go(3)} onNext={() => go(5)} /> : null}

            {step === 5 ? (
              <PreflightStep
                preflight={preflight}
                onRefresh={load}
                onBack={() => go(4)}
                onFinish={finish}
              />
            ) : null}
          </>
        ) : null}
      </main>
    </div>
  )
}

/* ------------------------------------------------------- step 1: admin --- */

/**
 * The password is changed before anything else is configured, because until it
 * is, the way into this bastion is a string that scrolled past in a container
 * log. The server refuses to mark setup finished while that is still true, so
 * this step is mandatory rather than merely first.
 */
function AdministratorStep({
  pristine,
  onSaved,
  onNext,
}: {
  /** The seeded account still holds the password it was created with. */
  pristine: boolean
  onSaved: () => void
  onNext: () => void
}) {
  const { user } = useAuth()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const tooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH
  const mismatch = confirm.length > 0 && confirm !== password
  const ready = password.length >= MIN_PASSWORD_LENGTH && confirm === password

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!user || !ready) return

    setBusy(true)
    setError(null)
    try {
      await updateUser(user.id, { password })
      setPassword('')
      setConfirm('')
      onSaved()
      onNext()
    } catch (err) {
      setError(errorMessage(err, 'Could not change the password.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save}>
      <Panel
        eyebrow="Step 1"
        title="Choose an administrator password"
        description={
          pristine
            ? 'This account still has the password it was created with — generated at first boot and printed to the server log, or taken from KUBEMG_ADMIN_PASSWORD. Setup will not finish until it is changed.'
            : 'This password has already been changed. Set a new one here or carry on.'
        }
      >
        <div className="flex flex-col gap-4 p-4">
          {error ? <Notice tone="error">{error}</Notice> : null}
          {pristine ? (
            <Notice tone="warn">
              Anyone who can read this server’s log can sign in as{' '}
              <span className="font-mono">{user?.username}</span> until this is changed.
            </Notice>
          ) : (
            <Notice tone="ok">The bootstrap password is no longer in force.</Notice>
          )}

          <Field
            label="New password"
            htmlFor="password"
            hint={`At least ${MIN_PASSWORD_LENGTH} characters.`}
            error={tooShort ? `Too short — ${MIN_PASSWORD_LENGTH} characters minimum.` : undefined}
          >
            <TextInput
              id="password"
              type="password"
              autoFocus
              autoComplete="new-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </Field>

          <Field
            label="Confirm"
            htmlFor="confirm"
            error={mismatch ? 'The two entries do not match.' : undefined}
          >
            <TextInput
              id="confirm"
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(event) => setConfirm(event.target.value)}
            />
          </Field>
        </div>

        <StepActions>
          {!pristine ? (
            <Button type="button" variant="ghost" onClick={onNext} className="mr-auto">
              Keep the current password
            </Button>
          ) : null}
          <Button type="submit" variant="primary" disabled={busy || !ready}>
            {busy ? 'Saving…' : 'Set password'}
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </Panel>
    </form>
  )
}

/* ----------------------------------------------------- step 2: address --- */

/**
 * The one field that decides whether any cluster ever connects. It is baked
 * verbatim into every rendered agent manifest, so a wrong value here does not
 * fail here — it fails minutes later, on somebody else's cluster, as an agent
 * that dials and never arrives.
 *
 * The warnings are the server's own, reused from the settings API rather than
 * restated, so the two surfaces cannot come to disagree about one address.
 */
function AddressStep({
  settings,
  onSettings,
  onBack,
  onNext,
}: {
  settings: SettingsResponse
  onSettings: (next: SettingsResponse) => void
  onBack: () => void
  onNext: () => void
}) {
  const [value, setValue] = useState(settings.overrides.public_url || '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      onSettings(await updateSettings({ public_url: value.trim() }))
      onNext()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the address.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save}>
      <Panel
        eyebrow="Step 2"
        title="Where do clusters reach this bastion?"
        description="Not the address your browser is using — the one a pod inside a target cluster can dial. It is written into every agent install command and every generated kubeconfig."
      >
        <div className="flex flex-col gap-4 p-4">
          {error ? <Notice tone="error">{error}</Notice> : null}
          {settings.warnings.map((warning) => (
            <Notice key={warning} tone="warn">
              {warning}
            </Notice>
          ))}

          <Field
            label="Server URL"
            htmlFor="public_url"
            hint={`Leave empty to use ${settings.defaults.public_url}.`}
          >
            <TextInput
              id="public_url"
              autoFocus
              className="font-mono text-[12.5px]"
              placeholder="https://kubemg.internal:8443"
              value={value}
              onChange={(event) => setValue(event.target.value)}
            />
          </Field>

          <Effective label="In use" value={settings.effective.public_url} />

          <p className="text-[12.5px] leading-relaxed text-muted">
            Every other name the certificate has to be valid for goes in{' '}
            <span className="font-mono text-faint">KUBEMG_TLS_HOSTS</span>, which is read at boot —
            a cluster dialling a name the certificate does not cover fails its handshake rather than
            reaching a wrong address.
          </p>
        </div>

        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save & continue'}
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </Panel>
    </form>
  )
}

/* ------------------------------------------------------ step 3: images --- */

/**
 * Where target clusters pull the agent from. This is the air-gap question:
 * point it at an internal mirror and a cluster with no route to the internet
 * still installs.
 */
function ImagesStep({
  settings,
  onSettings,
  onBack,
  onNext,
}: {
  settings: SettingsResponse
  onSettings: (next: SettingsResponse) => void
  onBack: () => void
  onNext: () => void
}) {
  const [image, setImage] = useState(settings.overrides.agent_image || '')
  const [namespace, setNamespace] = useState(settings.overrides.agent_namespace || '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      onSettings(
        await updateSettings({
          agent_image: image.trim(),
          agent_namespace: namespace.trim(),
        }),
      )
      onNext()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the agent settings.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save}>
      <Panel
        eyebrow="Step 3"
        title="Where does the agent come from?"
        description="The image your target clusters pull, and the namespace it installs into. This host never pulls it."
      >
        <div className="flex flex-col gap-4 p-4">
          {error ? <Notice tone="error">{error}</Notice> : null}

          <Field
            label="Agent image"
            htmlFor="agent_image"
            hint={`Leave empty to use ${settings.defaults.agent_image}.`}
          >
            <TextInput
              id="agent_image"
              className="font-mono text-[12.5px]"
              placeholder={settings.defaults.agent_image}
              value={image}
              onChange={(event) => setImage(event.target.value)}
            />
          </Field>

          <Field
            label="Agent namespace"
            htmlFor="agent_namespace"
            hint={`Leave empty to use ${settings.defaults.agent_namespace}.`}
          >
            <TextInput
              id="agent_namespace"
              className="font-mono text-[12.5px]"
              placeholder={settings.defaults.agent_namespace}
              value={namespace}
              onChange={(event) => setNamespace(event.target.value)}
            />
          </Field>

          <Notice tone="info">
            An air-gapped install mirrors three images and nothing else is fetched at runtime:{' '}
            <span className="font-mono">kubemg</span> and{' '}
            <span className="font-mono">postgres:16-alpine</span> on this host, and the agent above
            on your clusters. The console’s fonts are served from the binary, so no page calls an
            external host.
          </Notice>
        </div>

        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save & continue'}
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </Panel>
    </form>
  )
}

/* ------------------------------------------------------- step 4: trail --- */

/**
 * What the audit trail keeps and for how long, on the same panel the Audit
 * settings page uses. It is here rather than left to a page nobody visits
 * because retention is cheapest to decide before there is a trail to shorten:
 * lowering the window later deletes what is already in it.
 */
function TrailStep({
  settings,
  onSettings,
  onBack,
  onNext,
}: {
  settings: SettingsResponse
  onSettings: (next: SettingsResponse) => void
  onBack: () => void
  onNext: () => void
}) {
  const [verbs, setVerbs] = useState<string[] | null>(
    settings.overrides.audit_verbs_selected ? settings.overrides.audit_verbs : null,
  )
  const [recordSessions, setRecordSessions] = useState(settings.effective.record_exec_sessions)
  const [recordDiffs, setRecordDiffs] = useState(settings.effective.record_manifest_diffs)
  const [retention, setRetention] = useState(
    settings.overrides.audit_retention_days > 0 ? String(settings.overrides.audit_retention_days) : '',
  )
  const [recordingRetention, setRecordingRetention] = useState(
    settings.overrides.session_recording_retention_days > 0
      ? String(settings.overrides.session_recording_retention_days)
      : '',
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // An empty box means "the default", which the API spells as 0. Both windows
  // are bounded the same way the server bounds them, so the form says no before
  // the request does.
  const retentionDays = Number(retention.trim() || 0)
  const recordingDays = Number(recordingRetention.trim() || 0)
  const retentionValid = Number.isInteger(retentionDays) && retentionDays >= 0 && retentionDays <= 3650
  const recordingValid = Number.isInteger(recordingDays) && recordingDays >= 0 && recordingDays <= 3650

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!retentionValid || !recordingValid) return

    setBusy(true)
    setError(null)
    try {
      onSettings(
        await updateSettings({
          audit_retention_days: retentionDays,
          session_recording_retention_days: recordingDays,
          // No selection is sent as an empty array, which the API reads as
          // "every verb" rather than as "record nothing".
          audit_verbs: verbs ?? [],
          record_exec_sessions: recordSessions,
          record_manifest_diffs: recordDiffs,
        }),
      )
      onNext()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the audit settings.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save} className="flex flex-col gap-5">
      {error ? <Notice tone="error">{error}</Notice> : null}

      <AuditSettingsPanel
        settings={settings}
        selectedVerbs={verbs}
        onVerbsChange={setVerbs}
        recordSessions={recordSessions}
        onRecordSessionsChange={setRecordSessions}
        recordManifestDiffs={recordDiffs}
        onRecordManifestDiffsChange={setRecordDiffs}
        retentionDays={retention}
        onRetentionChange={setRetention}
        recordingRetentionDays={recordingRetention}
        onRecordingRetentionChange={setRecordingRetention}
        retentionError={retentionValid ? undefined : 'Between 1 and 3650 days, or empty for the default.'}
        recordingRetentionError={
          recordingValid ? undefined : 'Between 1 and 3650 days, or empty to follow the audit window.'
        }
      />

      <div className="card overflow-hidden">
        <p className="px-4 py-3 text-[13px] leading-relaxed text-muted">
          These are worth deciding now rather than later: shortening a retention window deletes what
          is already past it, so the cheapest moment to choose one is before there is a trail.
        </p>
        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !retentionValid || !recordingValid}
          >
            {busy ? 'Saving…' : 'Save & continue'}
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </div>
    </form>
  )
}

/* ----------------------------------------------------- step 5: sign-in --- */

/**
 * Federated sign-in, on the same panel the SSO settings page uses, and openly
 * skippable. Local accounts work without it; this is the step for an
 * organisation that would rather nobody had a second password at all.
 */
function SignInStep({ onBack, onNext }: { onBack: () => void; onNext: () => void }) {
  return (
    <div className="flex flex-col gap-5">
      <SsoSettingsPanel />

      <div className="card overflow-hidden">
        <p className="px-4 py-3 text-[13px] leading-relaxed text-muted">
          Optional. Skip it and everyone signs in with a local account you create under Users —
          a provider can be added from Settings at any point, and adding one does not disable the
          local accounts already in use.
        </p>
        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button type="button" variant="primary" onClick={onNext}>
            Continue
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </div>
    </div>
  )
}

/* --------------------------------------------------- step 6: preflight --- */

/**
 * What this install is, as opposed to what it was just told.
 *
 * Everything here was settled before the first request and cannot be changed
 * from a browser: TLS material read off a volume, the origin of the signing key,
 * whether recordings are encrypted. Each one carries the literal line to set, and
 * they are shown *before* the operator leaves rather than discovered afterwards
 * from a log — which is the same reason the terminal discloses recording before
 * the first keystroke.
 */
function PreflightStep({
  preflight,
  onRefresh,
  onBack,
  onFinish,
}: {
  preflight: SetupPreflight | null
  onRefresh: () => Promise<void>
  onBack: () => void
  onFinish: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const pristine = preflight?.admin_password_pristine ?? false

  async function finish() {
    setBusy(true)
    setError(null)
    try {
      await onFinish()
    } catch (err) {
      setError(errorMessage(err, 'Could not finish setup.'))
      // The most likely refusal is the password gate, and it is resolved on the
      // first step — so re-read the state rather than leaving a stale verdict on
      // screen next to an error explaining it.
      await onRefresh()
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <Panel
        eyebrow="Step 6"
        title="What setup could not decide for you"
        description="These were read once when the server started, from an environment it cannot rewrite. Nothing on this page can change them — this is where they are said out loud instead."
        bodyClassName="flex flex-col gap-3 p-4"
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
        {pristine ? (
          <Notice tone="warn">
            The bootstrap administrator still has its original password. Setup cannot be finished
            until it is changed — go back to step one.
          </Notice>
        ) : null}

        {preflight?.warnings.map((warning) => (
          <Notice key={warning} tone="warn">
            {warning}
          </Notice>
        ))}

        {preflight ? <DeploymentCheckList checks={preflight.checks} /> : null}
      </Panel>

      <div className="card overflow-hidden">
        <p className="px-4 py-3 text-[13px] leading-relaxed text-muted">
          Finishing stamps this install as configured. This wizard does not come back — every field
          in it lives on its own page under Settings from here on.
        </p>
        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button type="button" variant="primary" disabled={busy || pristine} onClick={finish}>
            {busy ? 'Finishing…' : 'Finish & add a cluster'}
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </div>
    </div>
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
