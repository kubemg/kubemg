import { useEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { ArrowRight, Download, KeyRound, Smartphone } from 'lucide-react'
import { beginKeyCheck, confirmMFA, enrollMFA, errorMessage, verifyMFA } from '../api/client'
import type { FactorProof, MFAChallenge, MFAConfirmResult, MFAEnrollment, MFASessionResponse } from '../api/types'
import { looksLikeTOTP, recoveryCodesFile } from '../lib/mfa'
import { createKeyCredential, keyErrorText, signWithKey, webauthnSupported } from '../lib/webauthn'
import { Button, CodeBlock, Field, Notice, TextInput } from './primitives'

/** A factor kind an account can set up. */
export type FactorKind = 'totp' | 'webauthn'

/** A key's refusal in words, or the server's when it was the server that refused. */
function keyFailure(err: unknown, fallback: string): string {
  return keyErrorText(err) ?? errorMessage(err, fallback)
}

/**
 * The second step of a sign-in, in the card the password step was in.
 *
 * Either half of it: proving a factor the account holds, or — when the account
 * must hold one and does not — enrolling one, which ends in the session. There
 * is no third state: the server never answers a right password with a session
 * and a challenge both.
 */
export function SignInSecondStep({
  challenge,
  account,
  onSession,
  onCancel,
}: {
  challenge: MFAChallenge
  /** Who is signing in, for the recovery codes file. */
  account: string
  onSession: (session: MFASessionResponse) => Promise<void> | void
  onCancel: () => void
}) {
  if (challenge.mfa_enrollment_required) {
    return <SignInEnrollment challenge={challenge} account={account} onSession={onSession} onCancel={onCancel} />
  }
  return <MfaCodeStep challenge={challenge} onSession={onSession} onCancel={onCancel} />
}

/**
 * Enrolling at sign-in. A security key is offered beside the app only where the
 * server says this install can use one and the browser can ask one; otherwise
 * the step goes straight to the app, as it always did.
 */
function SignInEnrollment({
  challenge,
  account,
  onSession,
  onCancel,
}: {
  challenge: MFAChallenge
  account: string
  onSession: (session: MFASessionResponse) => Promise<void> | void
  onCancel: () => void
}) {
  const offersKey = (challenge.methods?.includes('webauthn') ?? false) && webauthnSupported()
  const [kind, setKind] = useState<FactorKind | null>(offersKey ? null : 'totp')

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Set up a second factor</h2>
        <p className="mt-1.5 text-[13px] text-muted">
          {kind === null
            ? 'Your organisation requires one for this account. Choose what you will sign in with.'
            : kind === 'webauthn'
              ? 'Your organisation requires one for this account. Register a security key or a passkey on this device.'
              : 'Your organisation requires one for this account. Scan the code with an authenticator app, then type the six digits it shows.'}
        </p>
      </div>
      {kind === null ? (
        <FactorKindChoice onChoose={setKind} />
      ) : (
        <MfaEnrollBody
          kind={kind}
          challenge={challenge.challenge}
          account={account}
          onDone={async (result) => {
            if (result.session) await onSession(result.session)
          }}
        />
      )}
      <Button type="button" variant="ghost" onClick={onCancel} className="self-start">
        Use a different account
      </Button>
    </div>
  )
}

/** The two kinds of factor, as two buttons. */
export function FactorKindChoice({ onChoose }: { onChoose: (kind: FactorKind) => void }) {
  return (
    <div className="grid gap-2">
      <FactorKindButton
        icon={<KeyRound aria-hidden="true" className="size-4" />}
        title="Security key or passkey"
        body="A YubiKey, Touch ID, Windows Hello or a phone's passkey. Cannot be phished."
        onClick={() => onChoose('webauthn')}
      />
      <FactorKindButton
        icon={<Smartphone aria-hidden="true" className="size-4" />}
        title="Authenticator app"
        body="A six-digit code from 1Password, Google Authenticator or any TOTP app."
        onClick={() => onChoose('totp')}
      />
    </div>
  )
}

function FactorKindButton({
  icon,
  title,
  body,
  onClick,
}: {
  icon: ReactNode
  title: string
  body: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex min-w-0 items-start gap-3 rounded-control border border-line bg-surface p-3 text-left transition-colors hover:border-accent-line"
    >
      <span className="grid size-8 shrink-0 place-items-center rounded-control border border-line bg-raised text-muted">
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-[13.5px] font-semibold text-fg">{title}</span>
        <span className="mt-0.5 block text-[12.5px] leading-snug text-muted">{body}</span>
      </span>
    </button>
  )
}

/** Proving a factor the account already holds. */
export function MfaCodeStep({
  challenge,
  onSession,
  onCancel,
}: {
  challenge: MFAChallenge
  onSession: (session: MFASessionResponse) => Promise<void> | void
  onCancel: () => void
}) {
  // A server older than security keys sent no methods for an app-only account.
  const methods = challenge.methods ?? ['totp']
  const offersKey = methods.includes('webauthn') && webauthnSupported()
  const offersApp = methods.includes('totp')
  const offersRecovery = methods.includes('recovery')
  // A key first where there is one: it cannot be phished, and it is one touch.
  const [mode, setMode] = useState<'webauthn' | 'totp' | 'recovery'>(
    offersKey ? 'webauthn' : offersApp || !offersRecovery ? 'totp' : 'recovery',
  )
  const recovery = mode === 'recovery'
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // A sign-in finished by a recovery code says how many are left before the
  // console opens, because the next time it matters is the time the phone is gone.
  const [finished, setFinished] = useState<MFASessionResponse | null>(null)

  async function finish(session: MFASessionResponse) {
    if (session.recovery_codes_remaining !== undefined) {
      setFinished(session)
      setBusy(false)
      return
    }
    await onSession(session)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !code.trim()) return
    setBusy(true)
    setError(null)
    try {
      await finish(await verifyMFA(challenge.challenge, code))
    } catch (err) {
      setError(errorMessage(err, 'That code did not work.'))
      setCode('')
      setBusy(false)
    }
  }

  async function useKey() {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const proof = await signWithKey(await beginKeyCheck(challenge.challenge))
      await finish(await verifyMFA(challenge.challenge, '', proof))
    } catch (err) {
      setError(keyFailure(err, 'The key was not accepted.'))
      setBusy(false)
    }
  }

  function switchTo(next: typeof mode) {
    setMode(next)
    setCode('')
    setError(null)
  }

  // The other ways in, as the row under the form.
  const alternatives = (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex flex-wrap items-center gap-1">
        {offersKey && mode !== 'webauthn' ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => switchTo('webauthn')}>
            Use your security key
          </Button>
        ) : null}
        {offersApp && mode !== 'totp' ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => switchTo('totp')}>
            Use the authenticator app
          </Button>
        ) : null}
        {offersRecovery && mode !== 'recovery' ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => switchTo('recovery')}>
            Use a recovery code
          </Button>
        ) : null}
      </div>
      <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
        Start over
      </Button>
    </div>
  )

  if (finished) {
    const left = finished.recovery_codes_remaining ?? 0
    return (
      <div className="flex flex-col gap-4">
        <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Signed in with a recovery code</h2>
        <Notice tone={left <= 3 ? 'warn' : 'info'}>
          That code is spent. {left === 0 ? 'You have none left' : `You have ${left} left`} — make new
          ones from your profile, and set up your factor again if the phone or key is gone.
        </Notice>
        <Button type="button" variant="primary" className="h-10 w-full" onClick={() => void onSession(finished)}>
          Continue
          <ArrowRight aria-hidden="true" className="size-4" />
        </Button>
      </div>
    )
  }

  // Nothing this browser can answer with: a key-only account on a browser
  // without WebAuthn, or keys all bound to a host the console has left with no
  // recovery codes behind them. Said, rather than drawing a code box that
  // nothing will ever fill.
  if (!offersKey && !offersApp && !offersRecovery) {
    return (
      <div className="flex flex-col gap-4">
        <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Two-factor check</h2>
        <Notice tone="warn">
          {methods.includes('webauthn')
            ? 'Your second factor is a security key, and this browser cannot use one. Sign in from a browser that can.'
            : 'None of your second factors works at this address. Ask an administrator to reset it.'}
        </Notice>
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} className="self-start">
          Start over
        </Button>
      </div>
    )
  }

  if (mode === 'webauthn') {
    return (
      <div className="flex flex-col gap-4">
        <div>
          <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Two-factor check</h2>
          <p className="mt-1.5 text-[13px] text-muted">
            Use the security key or passkey you registered for kubemg — touch the key, or confirm on
            this device when the browser asks.
          </p>
        </div>
        {error ? <Notice tone="error">{error}</Notice> : null}
        <Button type="button" variant="primary" autoFocus disabled={busy} className="h-10 w-full" onClick={useKey}>
          <KeyRound aria-hidden="true" className="size-4" />
          {busy ? 'Waiting for the key…' : 'Use security key'}
        </Button>
        {alternatives}
      </div>
    )
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <div>
        <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Two-factor check</h2>
        <p className="mt-1.5 text-[13px] text-muted">
          {recovery
            ? 'Type one of the recovery codes you saved when you set up your factor. Each works once.'
            : 'Type the six-digit code your authenticator app shows for kubemg.'}
        </p>
      </div>
      <Field label={recovery ? 'Recovery code' : 'Code'} htmlFor="mfa-code">
        <TextInput
          id="mfa-code"
          name="code"
          autoFocus
          required
          autoComplete="one-time-code"
          inputMode={recovery ? 'text' : 'numeric'}
          placeholder={recovery ? 'xxxxx-xxxxx' : '123 456'}
          className="font-mono tracking-wider"
          value={code}
          onChange={(event) => setCode(event.target.value)}
        />
      </Field>
      {error ? <Notice tone="error">{error}</Notice> : null}
      <Button type="submit" variant="primary" disabled={busy} className="h-10 w-full">
        {busy ? 'Checking…' : 'Verify'}
        {busy ? null : <ArrowRight aria-hidden="true" className="size-4" />}
      </Button>
      {alternatives}
    </form>
  )
}

/**
 * Enrolling a factor: for an app, the secret as a QR code and as text and the
 * first code; for a key, the browser's own prompt. Then — for the first factor
 * an account has — the recovery codes, which are shown once and must be
 * acknowledged before anything moves on.
 *
 * One body for both places a factor is added: a sign-in that cannot finish
 * without one (with `challenge`) and the profile (with a session). Two copies
 * would drift on exactly the step people get wrong.
 */
export function MfaEnrollBody({
  kind = 'totp',
  challenge,
  name,
  currentProof,
  account,
  onDone,
}: {
  kind?: FactorKind
  challenge?: string
  name?: string
  /** Proof of a factor the account already holds, when adding another. */
  currentProof?: FactorProof
  account: string
  onDone: (result: MFAConfirmResult) => Promise<void> | void
}) {
  const [enrollment, setEnrollment] = useState<MFAEnrollment | null>(null)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmed, setConfirmed] = useState<MFAConfirmResult | null>(null)
  // The enrolment starts once per mount. React's development double-invoke
  // would otherwise mint two secrets and show the first, which the server has
  // already replaced.
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true
    enrollMFA({
      kind,
      challenge,
      name,
      current_code: currentProof?.code,
      current_webauthn: currentProof?.webauthn,
    })
      .then(setEnrollment)
      .catch((err) => setError(errorMessage(err, 'Could not start setting up the factor.')))
  }, [kind, challenge, name, currentProof])

  async function finishConfirm(result: MFAConfirmResult) {
    if (result.recovery_codes?.length) {
      setConfirmed(result)
      setBusy(false)
      return
    }
    await onDone(result)
  }

  async function registerKey() {
    if (busy || !enrollment?.webauthn) return
    setBusy(true)
    setError(null)
    try {
      const credential = await createKeyCredential(enrollment.webauthn)
      await finishConfirm(await confirmMFA({ factor_id: enrollment.factor_id, credential, challenge }))
    } catch (err) {
      setError(keyFailure(err, 'The key was not registered.'))
      setBusy(false)
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !enrollment) return
    if (!looksLikeTOTP(code)) {
      setError('Type the six digits the app shows.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await finishConfirm(await confirmMFA({ factor_id: enrollment.factor_id, code, challenge }))
    } catch (err) {
      setError(errorMessage(err, 'That code did not work.'))
      setCode('')
      setBusy(false)
    }
  }

  if (confirmed?.recovery_codes) {
    return (
      <RecoveryCodes
        codes={confirmed.recovery_codes}
        account={account}
        continueLabel={confirmed.session ? 'Continue to kubemg' : 'Done'}
        onContinue={() => onDone(confirmed)}
      />
    )
  }

  if (!enrollment) {
    return error ? <Notice tone="error">{error}</Notice> : <p className="text-[13px] text-muted">Preparing…</p>
  }

  if (enrollment.webauthn) {
    // The prompt waits for a click: Safari will not open it without one, and
    // a key that lights up unasked is a key people touch without reading.
    return (
      <div className="flex flex-col gap-4">
        <div className="flex items-start gap-3 rounded-card border border-line bg-raised p-4">
          <KeyRound aria-hidden="true" className="mt-0.5 size-5 shrink-0 text-muted" />
          <p className="text-[13px] leading-relaxed text-muted">
            The browser asks which key to use. Touch a security key, or confirm with this
            device&rsquo;s fingerprint, face or PIN to make a passkey. The key is bound to{' '}
            <span className="font-data text-fg">{enrollment.webauthn.publicKey.rp.id}</span> and answers
            nowhere else.
          </p>
        </div>
        {error ? <Notice tone="error">{error}</Notice> : null}
        <Button type="button" variant="primary" autoFocus disabled={busy} className="h-10 w-full" onClick={registerKey}>
          {busy ? 'Waiting for the key…' : 'Register key'}
        </Button>
      </div>
    )
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <div className="flex flex-col items-center gap-3 rounded-card border border-line bg-raised p-4">
        {/* The server draws the code with its own light quiet zone, so it reads
            the same on both decks: a scanner wants dark modules on a light
            field, and an inverted code fails on many phones. */}
        <img
          src={enrollment.qr ?? ''}
          alt="QR code to scan with an authenticator app"
          className="size-44 rounded-control"
        />
        <p className="text-center text-[12px] text-muted">Cannot scan it? Type this key into the app instead.</p>
      </div>
      <CodeBlock value={enrollment.secret ?? ''} label="Setup key" wrap />
      <Field label="Code from the app" htmlFor="mfa-enroll-code">
        <TextInput
          id="mfa-enroll-code"
          autoFocus
          autoComplete="one-time-code"
          inputMode="numeric"
          placeholder="123 456"
          className="font-mono tracking-wider"
          value={code}
          onChange={(event) => setCode(event.target.value)}
        />
      </Field>
      {error ? <Notice tone="error">{error}</Notice> : null}
      <Button type="submit" variant="primary" disabled={busy} className="h-10 w-full">
        {busy ? 'Checking…' : 'Confirm'}
      </Button>
    </form>
  )
}

/**
 * Recovery codes, shown once. Moving on needs an explicit "I have saved these":
 * a set dismissed unread is the reset ticket a week from now.
 */
export function RecoveryCodes({
  codes,
  account,
  continueLabel,
  onContinue,
}: {
  codes: string[]
  account: string
  continueLabel: string
  onContinue: () => Promise<void> | void
}) {
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)

  function download() {
    const blob = new Blob([recoveryCodesFile(codes, account, window.location.host)], { type: 'text/plain' })
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href
    link.download = `kubemg-recovery-codes-${account}.txt`
    link.click()
    URL.revokeObjectURL(href)
  }

  return (
    <div className="flex flex-col gap-4">
      <Notice tone="warn">
        Save these recovery codes now — they are not shown again. Each signs in once if you lose your
        phone or key. Keep them somewhere other than the phone or key itself.
      </Notice>
      <CodeBlock value={codes.join('\n')} label="Recovery codes" />
      <Button type="button" size="sm" onClick={download} className="self-start">
        <Download aria-hidden="true" className="size-3.5" />
        Download as a file
      </Button>
      <label htmlFor="mfa-codes-saved" className="flex items-start gap-2.5">
        <input
          id="mfa-codes-saved"
          type="checkbox"
          checked={saved}
          onChange={(event) => setSaved(event.target.checked)}
          className="mt-0.5 size-4 shrink-0 accent-[var(--color-accent)]"
        />
        <span className="text-[13.5px] text-fg">I have saved these codes</span>
      </label>
      <Button
        type="button"
        variant="primary"
        disabled={!saved || busy}
        className="h-10 w-full"
        onClick={async () => {
          setBusy(true)
          try {
            await onContinue()
          } finally {
            setBusy(false)
          }
        }}
      >
        {continueLabel}
      </Button>
    </div>
  )
}
