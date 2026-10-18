import { useState } from 'react'
import type { FormEvent } from 'react'
import { KeyRound } from 'lucide-react'
import { beginKeyCheck, errorMessage, regenerateRecoveryCodes, removeMFAFactor } from '../api/client'
import type { FactorProof, MFAFactor, MFAStatus } from '../api/types'
import { keyCanProve } from '../lib/mfa'
import { keyErrorText, signWithKey, webauthnSupported } from '../lib/webauthn'
import { FactorKindChoice, MfaEnrollBody, RecoveryCodes } from './MfaSteps'
import type { FactorKind } from './MfaSteps'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/** Asks one of the account's keys to sign, on the session, for a change to the factors. */
async function proveWithKey(): Promise<FactorProof> {
  return { webauthn: await signWithKey(await beginKeyCheck()) }
}

function proofFailure(err: unknown, fallback: string): string {
  return keyErrorText(err) ?? errorMessage(err, fallback)
}

/**
 * The proof a change to the factors themselves needs. Any current factor's code
 * or a recovery code — or, where the account holds one that works here, a touch
 * of its key: a session alone must not be able to add, remove or reissue what
 * stops the next sign-in.
 */
function CurrentProofField({
  value,
  onChange,
  id,
  withKey,
  busy,
  onKey,
}: {
  value: string
  onChange: (value: string) => void
  id: string
  /** Whether a key can stand in for the code. */
  withKey: boolean
  busy: boolean
  onKey: () => void
}) {
  return (
    <>
      <Field
        label="Current code"
        htmlFor={id}
        hint="From your authenticator app, or one of your recovery codes."
      >
        <TextInput
          id={id}
          autoFocus={!withKey}
          autoComplete="one-time-code"
          className="font-mono tracking-wider"
          value={value}
          onChange={(event) => onChange(event.target.value)}
        />
      </Field>
      {withKey ? (
        <Button type="button" size="sm" disabled={busy} onClick={onKey} className="self-start">
          <KeyRound aria-hidden="true" className="size-3.5" />
          Use your security key instead
        </Button>
      ) : null}
    </>
  )
}

/**
 * Adding a factor from the profile: an app or, where the install and the
 * browser can use one, a key. A second factor needs a proof of the first.
 */
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
  const keysHere = Boolean(status.webauthn?.available) && webauthnSupported()
  const [kind, setKind] = useState<FactorKind | null>(keysHere ? null : 'totp')
  const [name, setName] = useState('')
  const [currentCode, setCurrentCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // The enrolment starts on Continue, not on open: the kind, the name and, for
  // a second factor, the proof are what it is started with.
  const [proof, setProof] = useState<FactorProof | null>(null)
  const withKey = keyCanProve(status, webauthnSupported())

  async function continueWithKey() {
    setBusy(true)
    setError(null)
    try {
      setProof(await proveWithKey())
    } catch (err) {
      setError(proofFailure(err, 'The key was not accepted.'))
    } finally {
      setBusy(false)
    }
  }

  const ready = kind !== null && (!status.enrolled || currentCode.trim() !== '')
  return (
    <Sheet
      title={status.enrolled ? 'Add another factor' : 'Set up a second factor'}
      eyebrow="Your account"
      onClose={onClose}
      onSubmit={
        proof || kind === null
          ? undefined
          : (event: FormEvent<HTMLFormElement>) => {
              event.preventDefault()
              if (!ready) return
              setProof(status.enrolled ? { code: currentCode.trim() } : {})
            }
      }
      footer={
        proof || kind === null ? null : (
          <>
            <Button type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!ready || busy}>
              Continue
            </Button>
          </>
        )
      }
    >
      {proof && kind ? (
        <MfaEnrollBody
          kind={kind}
          name={name.trim() || undefined}
          currentProof={status.enrolled ? proof : undefined}
          account={account}
          onDone={() => onAdded()}
        />
      ) : kind === null ? (
        <>
          <p className="text-[13px] leading-relaxed text-muted">What will you sign in with?</p>
          <FactorKindChoice onChoose={setKind} />
        </>
      ) : (
        <>
          <p className="text-[13px] leading-relaxed text-muted">
            {kind === 'webauthn'
              ? 'A security key or a passkey — a YubiKey, Touch ID, Windows Hello, a phone — then asks for a touch after your password.'
              : 'An authenticator app — 1Password, Google Authenticator, Microsoft Authenticator, any that speaks TOTP — then asks for its six digits after your password.'}
          </p>
          {kind === 'totp' && status.webauthn && !status.webauthn.available && status.webauthn.reason ? (
            <Notice tone="info">{status.webauthn.reason}</Notice>
          ) : null}
          <Field label="Name" htmlFor="mfa-factor-name" hint="So you can tell two factors apart later.">
            <TextInput
              id="mfa-factor-name"
              maxLength={64}
              placeholder={kind === 'webauthn' ? 'YubiKey' : 'Work phone'}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          {error ? <Notice tone="error">{error}</Notice> : null}
          {status.enrolled ? (
            <CurrentProofField
              id="mfa-add-current"
              value={currentCode}
              onChange={setCurrentCode}
              withKey={withKey}
              busy={busy}
              onKey={continueWithKey}
            />
          ) : null}
        </>
      )}
    </Sheet>
  )
}

/** Removing one factor, confirmed by a current code or a key. */
export function RemoveFactorSheet({
  factor,
  status,
  onClose,
  onRemoved,
}: {
  factor: MFAFactor
  status: MFAStatus
  onClose: () => void
  onRemoved: (status: MFAStatus) => void
}) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // The only one, which takes the recovery codes too.
  const last = status.factors.length === 1

  async function remove(proof: () => Promise<FactorProof>) {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      onRemoved(await removeMFAFactor(factor.id, await proof()))
    } catch (err) {
      setError(proofFailure(err, 'Could not remove the factor.'))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title={`Remove ${factor.name}`}
      eyebrow="Your account"
      onClose={onClose}
      onSubmit={(event: FormEvent<HTMLFormElement>) => {
        event.preventDefault()
        if (code.trim()) void remove(async () => ({ code: code.trim() }))
      }}
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
      <CurrentProofField
        id="mfa-remove-code"
        value={code}
        onChange={setCode}
        withKey={keyCanProve(status, webauthnSupported())}
        busy={busy}
        onKey={() => void remove(proveWithKey)}
      />
    </Sheet>
  )
}

/** New recovery codes, confirmed by a current code or a key. The old set stops working. */
export function RecoveryCodesSheet({
  status,
  account,
  onClose,
  onReplaced,
}: {
  status: MFAStatus
  account: string
  onClose: () => void
  onReplaced: () => void
}) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)

  async function replace(proof: () => Promise<FactorProof>) {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      setCodes(await regenerateRecoveryCodes(await proof()))
    } catch (err) {
      setError(proofFailure(err, 'Could not make new recovery codes.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="New recovery codes"
      eyebrow="Your account"
      onClose={codes ? onReplaced : onClose}
      onSubmit={
        codes
          ? undefined
          : (event: FormEvent<HTMLFormElement>) => {
              event.preventDefault()
              if (code.trim()) void replace(async () => ({ code: code.trim() }))
            }
      }
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
          <CurrentProofField
            id="mfa-recovery-code"
            value={code}
            onChange={setCode}
            withKey={keyCanProve(status, webauthnSupported())}
            busy={busy}
            onKey={() => void replace(proveWithKey)}
          />
        </>
      )}
    </Sheet>
  )
}
