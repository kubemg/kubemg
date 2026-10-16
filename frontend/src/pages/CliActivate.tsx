import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router'
import { ArrowRight, CircleCheck, CircleX, SquareTerminal } from 'lucide-react'
import { decideCLIDeviceRequest, errorMessage, fetchCLIDeviceRequest } from '../api/client'
import type { CLIDeviceRequest } from '../api/types'
import { AuthLayout } from '../components/AuthLayout'
import { Button, DetailList, Field, Notice, Spinner, TextInput } from '../components/primitives'
import { formatInstant, relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'

/**
 * Approving a `kubemg login`.
 *
 * The CLI never sees a password or a second factor: it shows a code and opens
 * this page, and approving here — from a browser session that already passed
 * both — is what gives it a session, as the person approving. That makes this
 * page the device flow's one real defence. Somebody who starts a sign-in and
 * sends you the link gets *your* access if you approve it, so the page leads
 * with the code to compare against the terminal and with where the request
 * came from, and says when that is not where you are.
 *
 * It is drawn in the sign-in's frame, not the console's: it is the last step
 * of getting in, reached straight from a terminal, and one question asked
 * inside the whole console — cluster rail, search, footer — read as a detour.
 */
export function CliActivate() {
  const [params, setParams] = useSearchParams()
  const { user, signOut } = useAuth()
  const code = params.get('code') ?? ''

  return (
    <AuthLayout
      wide
      cardTestId="cli-activate-card"
      headline={
        <>
          A command line asked
          <br />
          to sign in as you.
        </>
      }
      lede="kubemg login never sees your password or your second factor. Approving here hands it a session as you: kubectl then gets fifteen-minute credentials, and every call is recorded under your name."
      footnote="Only approve a sign-in you started just now."
    >
      <CliActivateBody
        code={code}
        onCode={(next) => setParams({ code: next }, { replace: true })}
        account={user?.username}
        onSwitchAccount={signOut}
      />
    </AuthLayout>
  )
}

type Outcome = 'approved' | 'denied' | null

/** The card without its frame, so its decisions are testable alone. */
export function CliActivateBody({
  code,
  onCode,
  account,
  onSwitchAccount,
}: {
  code: string
  onCode: (code: string) => void
  /** Who approving signs the command line in as. */
  account?: string
  /** Signing out comes back here with the code, through the sign-in. */
  onSwitchAccount?: () => void
}) {
  const [typed, setTyped] = useState(code)
  const [request, setRequest] = useState<CLIDeviceRequest | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [outcome, setOutcome] = useState<Outcome>(null)

  useEffect(() => {
    if (!code) return
    let live = true
    setLoading(true)
    setError(null)
    setRequest(null)
    setOutcome(null)
    fetchCLIDeviceRequest(code)
      .then((answer) => {
        if (live) setRequest(answer)
      })
      .catch((err) => {
        if (live) setError(errorMessage(err, 'Could not find that sign-in.'))
      })
      .finally(() => {
        if (live) setLoading(false)
      })
    return () => {
      live = false
    }
  }, [code])

  async function decide(decision: 'approve' | 'deny') {
    if (!request || busy) return
    setBusy(true)
    setError(null)
    try {
      await decideCLIDeviceRequest(request.user_code, decision)
      setOutcome(decision === 'approve' ? 'approved' : 'denied')
    } catch (err) {
      setError(errorMessage(err, 'Could not record your answer.'))
    } finally {
      setBusy(false)
    }
  }

  function find(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (typed.trim()) onCode(typed.trim())
  }

  if (outcome) {
    const approved = outcome === 'approved'
    return (
      <div className="flex flex-col items-start">
        <span
          className={`grid size-10 place-items-center rounded-full ${approved ? 'bg-ok-soft text-ok' : 'bg-raised text-muted'}`}
        >
          {approved ? <CircleCheck aria-hidden="true" className="size-5" /> : <CircleX aria-hidden="true" className="size-5" />}
        </span>
        <h2 className="mt-4 text-[22px] font-bold tracking-[-0.02em] text-fg">
          {approved ? 'Signed in' : 'Sign-in denied'}
        </h2>
        <p className="mt-1.5 text-[13px] leading-relaxed text-muted">
          {approved
            ? `Go back to your terminal — kubemg finishes on its own within a few seconds. The session lasts ${request?.session_hours ?? 12} hours, and you can end it from My credentials.`
            : 'The command line was told no, and nothing was signed in.'}
        </p>
        <p className="mt-4 text-[13px] text-muted">You can close this tab.</p>
        <div className="mt-6 flex w-full flex-wrap gap-2">
          {approved ? (
            <Link
              to="/me/credentials"
              className="inline-flex h-10 flex-1 items-center justify-center gap-1.5 rounded-control border border-line bg-surface px-3 text-[13px] font-medium text-fg transition-colors hover:bg-raised"
            >
              My credentials
            </Link>
          ) : null}
          <Link
            to="/"
            className="inline-flex h-10 flex-1 items-center justify-center gap-1.5 rounded-control border border-line bg-surface px-3 text-[13px] font-medium text-fg transition-colors hover:bg-raised"
          >
            Open the console
            <ArrowRight aria-hidden="true" className="size-4" />
          </Link>
        </div>
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex items-center gap-2 text-muted">
        <SquareTerminal aria-hidden="true" className="size-4" />
        <span className="text-[12.5px] font-medium">kubemg login</span>
      </div>
      <h2 className="mt-2 text-[22px] font-bold tracking-[-0.02em] text-fg">Approve a CLI sign-in</h2>

      {!code || (error && !request) ? (
        <>
          <p className="mt-1.5 text-[13px] text-muted">Enter the code your terminal shows.</p>
          <form onSubmit={find} className="mt-6 flex flex-col gap-4">
            <Field label="Code shown by kubemg login" htmlFor="cli-code">
              <TextInput
                id="cli-code"
                autoFocus
                autoComplete="off"
                placeholder="BCDF-GHJK"
                className="font-mono tracking-wider uppercase"
                value={typed}
                onChange={(event) => setTyped(event.target.value)}
              />
            </Field>
            {error ? <Notice tone="error">{error}</Notice> : null}
            <Button type="submit" variant="primary" disabled={!typed.trim()} className="h-10 w-full">
              Continue
              <ArrowRight aria-hidden="true" className="size-4" />
            </Button>
          </form>
        </>
      ) : null}

      {loading ? (
        <p className="mt-6 flex items-center gap-2 text-[13px] text-muted">
          <Spinner className="size-4" />
          Looking up the sign-in…
        </p>
      ) : null}

      {request ? (
        <>
          <p className="mt-1.5 text-[13px] text-muted">Check that this code matches the one in your terminal.</p>
          <div className="mt-5 rounded-control border border-line bg-sunken px-4 py-3.5 text-center">
            <span className="font-mono text-[26px] font-semibold tracking-[0.18em] text-fg">{request.user_code}</span>
          </div>

          {request.same_address ? null : (
            <div className="mt-4">
              <Notice tone="warn">
                This request came from <span className="font-data">{request.source_ip}</span>, not from
                the address this browser is using. That is normal when you ran kubemg on a remote
                machine — and it is exactly what it looks like when somebody else started a sign-in
                and sent you the link. If you did not just run kubemg login, deny it.
              </Notice>
            </div>
          )}

          <div className="mt-5 border-t border-line-soft pt-5">
            <DetailList
              columns={2}
              rows={[
                { term: 'Machine', value: request.client_host || 'Not given' },
                { term: 'System', value: request.client_os || 'Not given' },
                {
                  term: 'Client',
                  value: [request.client_name, request.client_version].filter(Boolean).join(' ') || 'Not given',
                },
                {
                  term: 'From',
                  value: request.source_ip,
                  tone: request.same_address ? 'default' : 'warn',
                },
                {
                  term: 'Started',
                  value: <span title={formatInstant(request.created_at)}>{relativeAge(request.created_at)}</span>,
                },
                { term: 'Session', value: `${request.session_hours} hours` },
              ]}
            />
          </div>

          <p className="mt-5 text-[13px] leading-relaxed text-muted">
            Approving signs this command line in as {account ? <span className="font-data text-fg">{account}</span> : 'you'}{' '}
            for {request.session_hours} hours. It can then reach every cluster you can, through
            kubectl, with every call recorded under your name.
          </p>

          {error ? (
            <div className="mt-4">
              <Notice tone="error">{error}</Notice>
            </div>
          ) : null}

          <div className="mt-6 flex gap-2">
            <Button variant="danger" disabled={busy} onClick={() => void decide('deny')} className="h-10">
              Deny
            </Button>
            <Button variant="primary" disabled={busy} onClick={() => void decide('approve')} className="h-10 flex-1">
              {busy ? 'Recording…' : 'Approve sign-in'}
            </Button>
          </div>
        </>
      ) : null}

      {account && onSwitchAccount ? (
        <p className="mt-6 border-t border-line-soft pt-4 text-[12.5px] text-muted">
          Signed in as <span className="font-data text-fg">{account}</span>.{' '}
          <button
            type="button"
            onClick={onSwitchAccount}
            className="text-accent underline-offset-2 hover:underline"
          >
            Use another account
          </button>
        </p>
      ) : null}
    </div>
  )
}
