import { useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, regenerateRecoveryCodes, removeMFAFactor } from '../api/client'
import type { MFAFactor, MFAStatus } from '../api/types'
import { MfaEnrollBody, RecoveryCodes } from './MfaSteps'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/**
 * The code a change to the factors themselves needs. Any current factor's code
 * or a recovery code: a session alone must not be able to add, remove or reissue
 * what stops the next sign-in.
 */
function CurrentCodeField({ value, onChange, id }: { value: string; onChange: (value: string) => void; id: string }) {
  return (
    <Field
      label="Current code"
      htmlFor={id}
      hint="From your authenticator app, or one of your recovery codes."
    >
      <TextInput
        id={id}
        autoFocus
        autoComplete="one-time-code"
        className="font-mono tracking-wider"
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </Field>
  )
}

/** Adding a factor from the profile. A second one needs a code from the first. */
export function AddFactorSheet({
  status,
  account,
  onClose,
  onAdded,
}: {
  status: MFAStatus
  account: string
  onClose: () => void
  onAdded: () => void
}) {
  const [name, setName] = useState('')
  const [currentCode, setCurrentCode] = useState('')
  // The enrolment starts on Continue, not on open: the name and, for a second
  // factor, the code are what it is started with.
  const [started, setStarted] = useState(false)

  return (
    <Sheet
      title={status.enrolled ? 'Add another factor' : 'Set up a second factor'}
      eyebrow="Your account"
      onClose={onClose}
      onSubmit={
        started
          ? undefined
          : (event: FormEvent<HTMLFormElement>) => {
              event.preventDefault()
              if (status.enrolled && !currentCode.trim()) return
              setStarted(true)
            }
      }
      footer={
        started ? null : (
          <>
            <Button type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={status.enrolled && !currentCode.trim()}>
              Continue
            </Button>
          </>
        )
      }
    >
      {started ? (
        <MfaEnrollBody
          name={name.trim() || undefined}
          currentCode={status.enrolled ? currentCode.trim() : undefined}
          account={account}
          onDone={() => onAdded()}
        />
      ) : (
        <>
          <p className="text-[13px] leading-relaxed text-muted">
            An authenticator app — 1Password, Google Authenticator, Microsoft Authenticator, any that
            speaks TOTP — then asks for its six digits after your password.
          </p>
          <Field label="Name" htmlFor="mfa-factor-name" hint="So you can tell two factors apart later.">
            <TextInput
              id="mfa-factor-name"
              maxLength={64}
              placeholder="Work phone"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          {status.enrolled ? (
            <CurrentCodeField id="mfa-add-current" value={currentCode} onChange={setCurrentCode} />
          ) : null}
        </>
      )}
    </Sheet>
  )
}

/** Removing one factor, confirmed by a current code. */
export function RemoveFactorSheet({
  factor,
  last,
  onClose,
  onRemoved,
}: {
  factor: MFAFactor
  /** Whether it is the account's only one, which takes the recovery codes too. */
  last: boolean
  onClose: () => void
  onRemoved: (status: MFAStatus) => void
}) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !code.trim()) return
    setBusy(true)
    setError(null)
    try {
      onRemoved(await removeMFAFactor(factor.id, code.trim()))
    } catch (err) {
      setError(errorMessage(err, 'Could not remove the factor.'))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title={`Remove ${factor.name}`}
      eyebrow="Your account"
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="danger" disabled={busy || !code.trim()}>
            {busy ? 'Removing…' : 'Remove factor'}
          </Button>
        </>
      }
    >
      {last ? (
        <Notice tone="warn">
          This is your only factor. Your recovery codes go with it, and sign-in goes back to a
          password alone.
        </Notice>
      ) : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
      <CurrentCodeField id="mfa-remove-code" value={code} onChange={setCode} />
    </Sheet>
  )
}

/** New recovery codes, confirmed by a current code. The old set stops working. */
export function RecoveryCodesSheet({
  account,
  onClose,
  onReplaced,
}: {
  account: string
  onClose: () => void
  onReplaced: () => void
}) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !code.trim()) return
    setBusy(true)
    setError(null)
    try {
      setCodes(await regenerateRecoveryCodes(code.trim()))
    } catch (err) {
      setError(errorMessage(err, 'Could not make new recovery codes.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="New recovery codes"
      eyebrow="Your account"
      onClose={codes ? onReplaced : onClose}
      onSubmit={codes ? undefined : submit}
      footer={
        codes ? null : (
          <>
            <Button type="button" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !code.trim()}>
              {busy ? 'Making…' : 'Make new codes'}
            </Button>
          </>
        )
      }
    >
      {codes ? (
        <RecoveryCodes codes={codes} account={account} continueLabel="Done" onContinue={onReplaced} />
      ) : (
        <>
          <p className="text-[13px] leading-relaxed text-muted">
            Ten new codes replace the ones you have — every old code stops working, used or not.
          </p>
          {error ? <Notice tone="error">{error}</Notice> : null}
          <CurrentCodeField id="mfa-recovery-code" value={code} onChange={setCode} />
        </>
      )}
    </Sheet>
  )
}
