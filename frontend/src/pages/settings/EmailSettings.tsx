import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw, Send, Trash2 } from 'lucide-react'
import {
  deleteSMTPRelay,
  errorMessage,
  fetchSMTPRelay,
  saveSMTPRelay,
  testSMTPRelay,
} from '../../api/client'
import type { SMTPRelay, SMTPSecurity } from '../../api/types'
import { Button, Field, Notice, Panel, Select, TextInput } from '../../components/primitives'
import { SettingsAside, SettingsLayout } from '../../components/settings/SettingsLayout'
import { useUnsavedGuard } from '../../lib/unsavedGuard'
import { useAuth } from '../../state/auth-context'
import { useConfirm } from '../../state/confirm-context'

/** The usual port for each security mode, offered when the port is untouched. */
const DEFAULT_PORT: Record<SMTPSecurity, number> = { starttls: 587, tls: 465, none: 25 }

const SECURITY: { value: SMTPSecurity; label: string; hint: string }[] = [
  {
    value: 'starttls',
    label: 'STARTTLS',
    hint: 'Connects in plain text and upgrades to TLS before anything is sent. A relay that does not offer the upgrade is refused, never used unencrypted.',
  },
  { value: 'tls', label: 'TLS', hint: 'TLS from the first byte, usually on port 465.' },
  {
    value: 'none',
    label: 'None',
    hint: 'Plain text. Only for a relay that needs no login; a password is never sent this way except to this host.',
  },
]

interface Draft {
  host: string
  port: string
  security: SMTPSecurity
  username: string
  /** What was typed; empty with `keepPassword` means "leave the stored one". */
  password: string
  keepPassword: boolean
  from: string
}

function draftOf(relay: SMTPRelay | null): Draft {
  return {
    host: relay?.host ?? '',
    port: relay?.port ? String(relay.port) : '587',
    security: relay?.security ?? 'starttls',
    username: relay?.username ?? '',
    password: '',
    keepPassword: Boolean(relay?.has_password),
    from: relay?.from ?? '',
  }
}

function sameDraft(a: Draft, b: Draft): boolean {
  return (
    a.host.trim() === b.host.trim() &&
    a.port.trim() === b.port.trim() &&
    a.security === b.security &&
    a.username.trim() === b.username.trim() &&
    a.password === b.password &&
    a.keepPassword === b.keepPassword &&
    a.from.trim() === b.from.trim()
  )
}

/**
 * The mail relay. KubeMG sends one kind of mail today — reminders before a
 * password expires — and sends it through one SMTP relay configured here. The
 * password is write-only and stored sealed; saving never dials the relay, a
 * test message is its own act.
 */
export function EmailSettings() {
  const { user } = useAuth()
  const confirm = useConfirm()
  const [relay, setRelay] = useState<SMTPRelay | null>(null)
  const [draft, setDraft] = useState<Draft>(draftOf(null))
  const [testTo, setTestTo] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const next = await fetchSMTPRelay()
      setRelay(next)
      setDraft(draftOf(next.configured ? next : null))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the mail relay.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const stored = relay?.configured ? relay : null
  const dirty = relay !== null && !sameDraft(draft, draftOf(stored))
  useUnsavedGuard(dirty, 'the mail relay')

  const port = Number(draft.port.trim())
  const portError =
    draft.port.trim() === '' || (Number.isInteger(port) && port >= 1 && port <= 65535)
      ? undefined
      : 'Enter a port between 1 and 65535.'

  function update(patch: Partial<Draft>) {
    setDraft((current) => ({ ...current, ...patch }))
    setNotice(null)
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (portError) {
      document.getElementById('smtp-port')?.focus()
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const next = await saveSMTPRelay({
        host: draft.host.trim(),
        port: port || DEFAULT_PORT[draft.security],
        security: draft.security,
        username: draft.username.trim(),
        // Omitted keeps the stored password; typed replaces it; cleared removes it.
        ...(draft.keepPassword && draft.password === '' ? {} : { password: draft.password }),
        from: draft.from.trim(),
      })
      setRelay(next)
      setDraft(draftOf(next))
      setNotice('Saved. Send a test message to check the relay accepts it.')
    } catch (err) {
      setError(errorMessage(err, 'Could not save the mail relay.'))
    } finally {
      setBusy(false)
    }
  }

  async function test() {
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const result = await testSMTPRelay(testTo.trim() || undefined)
      setNotice(`The relay accepted a test message for ${result.to}.`)
    } catch (err) {
      setError(errorMessage(err, 'The test message was not sent.'))
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    const ok = await confirm({
      title: 'Remove mail relay',
      body: 'KubeMG stops sending mail. Passwords still expire on schedule — nobody is reminded before theirs does.',
      confirmLabel: 'Remove',
      tone: 'danger',
    })
    if (!ok) return
    setBusy(true)
    setError(null)
    try {
      await deleteSMTPRelay()
      await load()
      setNotice('The mail relay was removed.')
    } catch (err) {
      setError(errorMessage(err, 'Could not remove the mail relay.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <SettingsLayout
      title="Email"
      description="The SMTP relay KubeMG sends its own mail through — today, reminders before a password expires."
      aside={
        relay ? (
          <>
            <SettingsAside
              label="Relay in force"
              value={stored ? `${stored.host}:${stored.port}` : 'None — no mail is sent'}
              reach="Reminders go 14, 7, 3 and 1 days before a password expires, and once when it has, to the account's own email address. An account with no address gets none."
            />
            {stored ? (
              <SettingsAside
                label="Sends as"
                value={stored.from}
                reach={stored.username ? `Signs in to the relay as ${stored.username}.` : 'Sends without a login.'}
              />
            ) : null}
          </>
        ) : null
      }
      actions={
        relay ? (
          <>
            <Button
              type="button"
              variant="ghost"
              disabled={busy || !dirty}
              onClick={() => {
                setDraft(draftOf(stored))
                setNotice(null)
              }}
            >
              <RotateCcw aria-hidden="true" className="size-4" />
              Discard
            </Button>
            <Button type="submit" form="email-settings-form" variant="primary" disabled={busy || !dirty}>
              {busy ? 'Saving…' : 'Save relay'}
            </Button>
          </>
        ) : null
      }
    >
      <form id="email-settings-form" onSubmit={save} className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {notice && !dirty ? <Notice tone="ok">{notice}</Notice> : null}
        {loading ? <p className="text-[13px] text-muted">Loading…</p> : null}

        {relay ? (
          <Panel
            eyebrow="Outgoing mail"
            title="SMTP relay"
            description="Where KubeMG hands its mail over. The relay delivers it; KubeMG never connects to a recipient's server itself."
            bodyClassName="flex flex-col gap-4 p-4"
          >
            <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
              <Field label="Host" htmlFor="smtp-host" hint="A host name or address, without a scheme or port.">
                <TextInput
                  id="smtp-host"
                  className="font-data text-[12.5px]"
                  placeholder="smtp.example.com"
                  value={draft.host}
                  onChange={(event) => update({ host: event.target.value })}
                />
              </Field>
              <Field label="Port" htmlFor="smtp-port" error={portError}>
                <TextInput
                  id="smtp-port"
                  type="number"
                  min={1}
                  max={65535}
                  inputMode="numeric"
                  className="font-data text-[12.5px]"
                  value={draft.port}
                  onChange={(event) => update({ port: event.target.value })}
                />
              </Field>
            </div>
            <Field
              label="Security"
              htmlFor="smtp-security"
              hint={SECURITY.find((option) => option.value === draft.security)?.hint}
            >
              <Select
                id="smtp-security"
                className="max-w-60"
                value={draft.security}
                onChange={(event) => {
                  const security = event.target.value as SMTPSecurity
                  // Follow the mode's usual port unless one was typed by hand.
                  const untouched = Object.values(DEFAULT_PORT).map(String).includes(draft.port.trim())
                  update({ security, ...(untouched ? { port: String(DEFAULT_PORT[security]) } : {}) })
                }}
              >
                {SECURITY.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Field
              label="Sender address"
              htmlFor="smtp-from"
              hint="The From of every message, for example KubeMG <kubemg@example.com>."
            >
              <TextInput
                id="smtp-from"
                className="font-data text-[12.5px]"
                placeholder="kubemg@example.com"
                value={draft.from}
                onChange={(event) => update({ from: event.target.value })}
              />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Username" htmlFor="smtp-username" hint="Leave empty for a relay that needs no login.">
                <TextInput
                  id="smtp-username"
                  autoComplete="off"
                  className="font-data text-[12.5px]"
                  value={draft.username}
                  onChange={(event) => update({ username: event.target.value })}
                />
              </Field>
              <Field
                label="Password"
                htmlFor="smtp-password"
                hint={
                  draft.keepPassword
                    ? 'A password is stored. Type to replace it.'
                    : 'Stored encrypted and never shown again.'
                }
              >
                <TextInput
                  id="smtp-password"
                  type="password"
                  autoComplete="new-password"
                  placeholder={draft.keepPassword ? '••••••••' : ''}
                  value={draft.password}
                  disabled={draft.username.trim() === ''}
                  onChange={(event) => update({ password: event.target.value })}
                />
              </Field>
            </div>
            {draft.keepPassword ? (
              <button
                type="button"
                onClick={() => update({ keepPassword: false, password: '' })}
                className="self-start text-[12.5px] text-muted transition-colors hover:text-fg hover:underline"
              >
                Clear the stored password
              </button>
            ) : null}
          </Panel>
        ) : null}

        {stored ? (
          <Panel
            eyebrow="Check"
            title="Send a test message"
            description="Sends one message through the saved relay — save changes first. A refusal is shown in the relay's own words."
            bodyClassName="flex flex-col gap-4 p-4"
          >
            <Field
              label="Recipient"
              htmlFor="smtp-test-to"
              hint={user?.email ? `Leave empty to send to ${user.email}.` : 'Your account has no email address, so name one.'}
            >
              <TextInput
                id="smtp-test-to"
                className="font-data text-[12.5px]"
                placeholder={user?.email ?? 'you@example.com'}
                value={testTo}
                onChange={(event) => setTestTo(event.target.value)}
              />
            </Field>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <Button type="button" disabled={busy || dirty} onClick={() => void test()}>
                <Send aria-hidden="true" className="size-4" />
                Send test message
              </Button>
              <Button type="button" variant="danger" disabled={busy} onClick={() => void remove()}>
                <Trash2 aria-hidden="true" className="size-4" />
                Remove relay
              </Button>
            </div>
          </Panel>
        ) : null}
      </form>
    </SettingsLayout>
  )
}
