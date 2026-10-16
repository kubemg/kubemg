import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ArrowRight, Download } from 'lucide-react'
import { confirmMFA, enrollMFA, errorMessage, verifyMFA } from '../api/client'
import type { MFAChallenge, MFAConfirmResult, MFAEnrollment, MFASessionResponse } from '../api/types'
import { looksLikeTOTP, recoveryCodesFile } from '../lib/mfa'
import { Button, CodeBlock, Field, Notice, TextInput } from './primitives'

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
    return (
      <div className="flex flex-col gap-4">
        <div>
          <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Set up a second factor</h2>
          <p className="mt-1.5 text-[13px] text-muted">
            Your organisation requires one for this account. Scan the code with an authenticator
            app, then type the six digits it shows.
          </p>
        </div>
        <MfaEnrollBody
          challenge={challenge.challenge}
          account={account}
          onDone={async (result) => {
            if (result.session) await onSession(result.session)
          }}
        />
        <Button type="button" variant="ghost" onClick={onCancel} className="self-start">
          Use a different account
        </Button>
      </div>
    )
  }
  return <MfaCodeStep challenge={challenge} onSession={onSession} onCancel={onCancel} />
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
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // A sign-in finished by a recovery code says how many are left before the
  // console opens, because the next time it matters is the time the phone is gone.
  const [finished, setFinished] = useState<MFASessionResponse | null>(null)
  const offersRecovery = challenge.methods?.includes('recovery') ?? false

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !code.trim()) return
    setBusy(true)
    setError(null)
    try {
      const session = await verifyMFA(challenge.challenge, code)
      if (session.recovery_codes_remaining !== undefined) {
        setFinished(session)
        setBusy(false)
        return
      }
      await onSession(session)
    } catch (err) {
      setError(errorMessage(err, 'That code did not work.'))
      setCode('')
      setBusy(false)
    }
  }

  if (finished) {
    const left = finished.recovery_codes_remaining ?? 0
    return (
      <div className="flex flex-col gap-4">
        <h2 className="text-[22px] font-bold tracking-[-0.02em] text-fg">Signed in with a recovery code</h2>
        <Notice tone={left <= 3 ? 'warn' : 'info'}>
          That code is spent. {left === 0 ? 'You have none left' : `You have ${left} left`} — make new
          ones from your profile, and set up your authenticator app again if the phone is gone.
        </Notice>
        <Button type="button" variant="primary" className="h-10 w-full" onClick={() => void onSession(finished)}>
          Continue
          <ArrowRight aria-hidden="true" className="size-4" />
        </Button>
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
      <div className="flex flex-wrap items-center justify-between gap-2">
        {offersRecovery ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              setRecovery((value) => !value)
              setCode('')
              setError(null)
            }}
          >
            {recovery ? 'Use the authenticator app' : 'Use a recovery code'}
          </Button>
        ) : (
          <span />
        )}
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Start over
        </Button>
      </div>
    </form>
  )
}

/**
 * Enrolling a factor: the secret as a QR code and as text, the first code, and
 * — for the first factor an account has — the recovery codes, which are shown
 * once and must be acknowledged before anything moves on.
 *
 * One body for both places a factor is added: a sign-in that cannot finish
 * without one (with `challenge`) and the profile (with a session). Two copies
 * would drift on exactly the step people get wrong.
 */
export function MfaEnrollBody({
  challenge,
  name,
  currentCode,
  account,
  onDone,
}: {
  challenge?: string
  name?: string
  /** A code from a factor the account already holds, when adding another. */
  currentCode?: string
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
    enrollMFA({ challenge, name, current_code: currentCode })
      .then(setEnrollment)
      .catch((err) => setError(errorMessage(err, 'Could not start setting up the factor.')))
  }, [challenge, name, currentCode])

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
      const result = await confirmMFA({ factor_id: enrollment.factor_id, code, challenge })
      if (result.recovery_codes?.length) {
        setConfirmed(result)
        setBusy(false)
        return
      }
      await onDone(result)
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

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <div className="flex flex-col items-center gap-3 rounded-card border border-line bg-raised p-4">
        {/* The server draws the code with its own light quiet zone, so it reads
            the same on both decks: a scanner wants dark modules on a light
            field, and an inverted code fails on many phones. */}
        <img
          src={enrollment.qr}
          alt="QR code to scan with an authenticator app"
          className="size-44 rounded-control"
        />
        <p className="text-center text-[12px] text-muted">Cannot scan it? Type this key into the app instead.</p>
      </div>
      <CodeBlock value={enrollment.secret} label="Setup key" wrap />
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
        Save these recovery codes now — they are not shown again. Each signs in once if you lose the
        phone. Keep them somewhere other than the phone itself.
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
