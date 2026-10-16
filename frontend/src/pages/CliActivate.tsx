import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { Terminal } from 'lucide-react'
import { decideCLIDeviceRequest, errorMessage, fetchCLIDeviceRequest } from '../api/client'
import type { CLIDeviceRequest } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, DetailList, Field, Notice, Panel, TextInput } from '../components/primitives'
import { formatInstant, relativeAge } from '../lib/time'

/**
 * Approving a `kubemg login`.
 *
 * The CLI never sees a password or a second factor: it shows a code and opens
 * this page, and approving here — from a browser session that already passed
 * both — is what gives it a session, as the person approving. That makes this
 * page the device flow's one real defence. Somebody who starts a sign-in and
 * sends you the link gets *your* access if you approve it, so the page leads
 * with where the request came from and says when that is not where you are.
 */
export function CliActivate() {
  const [params, setParams] = useSearchParams()
  const code = params.get('code') ?? ''

  return (
    <AppShell
      title="Approve a CLI sign-in"
      description="A kubemg command line asked to sign in as you. Approve it only if you started it just now."
    >
      <CliActivateBody
        code={code}
        onCode={(next) => setParams({ code: next }, { replace: true })}
      />
    </AppShell>
  )
}

type Outcome = 'approved' | 'denied' | null

/** The page without its shell, so its decisions are testable alone. */
export function CliActivateBody({ code, onCode }: { code: string; onCode: (code: string) => void }) {
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

  if (outcome === 'approved') {
    return (
      <Notice tone="ok">
        Approved. Go back to your terminal — kubemg finishes signing in on its own within a few
        seconds. The session lasts {request?.session_hours ?? 12} hours and is listed under My
        credentials, where you can end it.
      </Notice>
    )
  }
  if (outcome === 'denied') {
    return <Notice tone="info">Denied. The command line was told no, and nothing was signed in.</Notice>
  }

  return (
    <div className="flex max-w-2xl min-w-0 flex-col gap-4">
      {!code || (error && !request) ? (
        <Panel title="Enter the code" bodyClassName="p-4">
          <form onSubmit={find} className="flex flex-wrap items-end gap-3">
            <div className="min-w-48 flex-1">
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
            </div>
            <Button type="submit" variant="primary" disabled={!typed.trim()}>
              Find
            </Button>
          </form>
        </Panel>
      ) : null}

      {error ? <Notice tone="error">{error}</Notice> : null}
      {loading ? <p className="text-[13px] text-muted">Looking up the sign-in…</p> : null}

      {request ? (
        <>
          {request.same_address ? null : (
            <Notice tone="warn">
              This request came from <span className="font-data">{request.source_ip}</span>, not from
              the address this browser is using. That is normal when you ran kubemg on a remote
              machine — and it is exactly what it looks like when somebody else started a sign-in
              and sent you the link. If you did not just run kubemg login, deny it.
            </Notice>
          )}
          <Panel
            title="The command line asking"
            description="What it said about itself, and where the server saw it."
            actions={<Terminal aria-hidden="true" className="size-4 text-muted" />}
            bodyClassName="p-4"
          >
            <DetailList
              columns={2}
              rows={[
                { term: 'Code', value: request.user_code },
                { term: 'Machine', value: request.client_host || 'Not given' },
                {
                  term: 'Client',
                  value: [request.client_name, request.client_version].filter(Boolean).join(' ') || 'Not given',
                },
                { term: 'System', value: request.client_os || 'Not given' },
                {
                  term: 'From',
                  value: request.source_ip,
                  tone: request.same_address ? 'default' : 'warn',
                },
                {
                  term: 'Started',
                  value: <span title={formatInstant(request.created_at)}>{relativeAge(request.created_at)}</span>,
                },
              ]}
            />
          </Panel>
          <p className="text-[13px] leading-relaxed text-muted">
            Approving signs this command line in as you for {request.session_hours} hours. It can
            then reach every cluster you can, through kubectl, with every call recorded under your
            name. You can end it any time from My credentials.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" disabled={busy} onClick={() => void decide('approve')}>
              {busy ? 'Recording…' : 'Approve sign-in'}
            </Button>
            <Button variant="danger" disabled={busy} onClick={() => void decide('deny')}>
              Deny
            </Button>
          </div>
        </>
      ) : null}
    </div>
  )
}
