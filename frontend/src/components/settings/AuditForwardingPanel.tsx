import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, Pencil, Plus, Send, Trash2, XCircle } from 'lucide-react'
import {
  createAuditForwarder,
  deleteAuditForwarder,
  errorMessage,
  fetchAuditForwarders,
  testAuditForwarder,
  updateAuditForwarder,
} from '../../api/client'
import type {
  AuditForwarder,
  AuditForwarderInput,
  AuditForwarderProtocol,
} from '../../api/types'
import { relativeAge } from '../../lib/time'
import {
  Button,
  Field,
  IconButton,
  Notice,
  Panel,
  Pill,
  Select,
  Sheet,
  TextArea,
  TextInput,
} from '../primitives'
import { useConfirm } from '../../state/confirm-context'
import { useResult } from '../../state/result-context'

/**
 * Where the complete audit trail is pushed.
 *
 * This is deliberately not an alarm channel, and the panel says so, because the
 * two look identical from a distance and choosing wrong is expensive. An alarm
 * is an alerting path: it deduplicates by fingerprint, holds a cool-off, and
 * drops signals when it backs up — every one of which loses records. A forwarder
 * ships every record, and the verb selection above it does not narrow what
 * leaves.
 *
 * There is no credential box. Syslog authenticates by network position or by
 * TLS, so the row reads back whole — including the CA bundle, which is a public
 * certificate and which an operator has to be able to check.
 */

const PROTOCOL_LABEL: Record<AuditForwarderProtocol, string> = {
  tcp: 'TCP',
  udp: 'UDP',
  tls: 'TCP over TLS',
}

/** What each transport does and does not guarantee, shown where it is chosen.
    The failure mode being warned about is real: a UDP forwarder that has been
    delivering to nothing looks exactly like one that works. */
const PROTOCOL_HINT: Record<AuditForwarderProtocol, string> = {
  tcp: 'A stream, so a record either arrives whole or the connection fails and you are told. Logsign listens for syslog on TCP 515.',
  udp: 'Fire-and-forget: nothing comes back, oversized records are truncated, and a collector that stopped listening is indistinguishable from one that is working. Logsign listens on UDP 514. Prefer TCP for a trail.',
  tls: 'TCP inside TLS. This is what shipping a trail across anything but a datacentre link has to be — the records name people, clusters and the namespaces they touched.',
}

const EMPTY_FORWARDER: AuditForwarderInput = {
  name: '',
  kind: 'syslog',
  host: '',
  // 0 asks the server for the protocol's default, which differs between TCP and
  // UDP. Picking one here would silently be wrong for the other.
  port: 0,
  protocol: 'tcp',
  facility: 16,
  app_name: 'kubemg',
  octet_counting: false,
  tls_ca_bundle: '',
  tls_insecure_skip_verify: false,
  enabled: true,
}

export function AuditForwardingPanel() {
  const confirm = useConfirm()
  const report = useResult()
  const [forwarders, setForwarders] = useState<AuditForwarder[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<AuditForwarder | 'new' | null>(null)
  // The verdict of the last Test, keyed by forwarder so two rows cannot claim it.
  const [tested, setTested] = useState<Record<number, { ok: boolean; message: string; note?: string }>>({})
  const [testing, setTesting] = useState<number | null>(null)

  const load = useCallback(async () => {
    try {
      const list = await fetchAuditForwarders()
      setForwarders(list.forwarders)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the audit forwarders.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function runTest(forwarder: AuditForwarder) {
    setTesting(forwarder.id)
    try {
      const verdict = await testAuditForwarder(forwarder.id)
      setTested((current) => ({ ...current, [forwarder.id]: verdict }))
      // The attempt is recorded on the row, so delivery health has to catch up
      // with what just happened.
      void load()
    } catch (err) {
      setTested((current) => ({
        ...current,
        [forwarder.id]: { ok: false, message: errorMessage(err, 'The test could not be sent.') },
      }))
    } finally {
      setTesting(null)
    }
  }

  async function remove(forwarder: AuditForwarder) {
    const confirmed = await confirm({
      eyebrow: 'Audit forwarding',
      title: `Delete “${forwarder.name}”?`,
      body: 'Records stop being sent there from the moment it goes. Nothing already delivered is affected, and the trail itself — the table and this server’s own log — is untouched.',
      confirmLabel: 'Delete',
    })
    if (!confirmed) return

    try {
      await deleteAuditForwarder(forwarder.id)
      await load()
      report({ tone: 'ok', title: `Deleted ${forwarder.name}`, body: 'Nothing is sent there any more.' })
    } catch (err) {
      const message = errorMessage(err, 'Could not delete the forwarder.')
      setError(message)
      report({ tone: 'error', title: `${forwarder.name} was not deleted`, body: message })
    }
  }

  return (
    <>
      <Panel
        eyebrow="Audit forwarding"
        title="Where the trail is shipped"
        description="Every audit record is pushed here as RFC 5424 syslog with a JSON message — the same fields this server writes to its own log, so one parser reads both copies. This is not an alarm: nothing is deduplicated and no verb selection narrows it."
        bodyClassName="flex flex-col"
        actions={
          <Button size="sm" onClick={() => setEditing('new')}>
            <Plus aria-hidden="true" className="size-3.5" />
            Add destination
          </Button>
        }
      >
        {error ? (
          <div className="px-4 pt-4">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        {loading ? <p className="px-4 py-6 text-[13px] text-muted">Loading…</p> : null}

        {!loading && forwarders.length === 0 ? (
          <p className="px-4 py-6 text-[13px] leading-relaxed text-muted">
            Nothing is being pushed. The trail is still complete in the table above and in this
            server’s own log stream — add a destination when a SIEM cannot come and collect it.
          </p>
        ) : null}

        <ul className="divide-y divide-line-soft">
          {forwarders.map((forwarder) => {
            const verdict = tested[forwarder.id]
            return (
              <li key={forwarder.id} className="flex flex-wrap items-start gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[13.5px] font-medium text-fg">
                      {forwarder.name}
                    </span>
                    <Pill tone="accent">{PROTOCOL_LABEL[forwarder.protocol]}</Pill>
                    {forwarder.enabled ? null : <Pill tone="idle">disabled</Pill>}
                    {forwarder.protocol === 'tls' && forwarder.tls_insecure_skip_verify ? (
                      <Pill tone="warn">certificate not verified</Pill>
                    ) : null}
                  </div>
                  <p className="mt-1 truncate font-mono text-[12px] text-muted">
                    {forwarder.host}:{forwarder.port} · {forwarder.app_name} · facility{' '}
                    {forwarder.facility}
                  </p>

                  {/* Delivery health. A forwarder that stopped working is
                      invisible by construction: nothing is missing from anywhere
                      an operator looks, records simply stop arriving somewhere
                      nobody watches. */}
                  {forwarder.last_status ? (
                    <p
                      className={`mt-1 text-[12px] ${
                        forwarder.last_status === 'ok' ? 'text-muted' : 'text-danger'
                      }`}
                    >
                      Last attempt {relativeAge(forwarder.last_attempt_at)} ·{' '}
                      {forwarder.last_status === 'ok'
                        ? 'delivered'
                        : forwarder.last_message || 'failed'}
                    </p>
                  ) : null}

                  {verdict ? (
                    <p
                      className={`mt-1 flex items-baseline gap-1.5 text-[12px] ${
                        verdict.ok ? 'text-ok' : 'text-danger'
                      }`}
                    >
                      {verdict.ok ? (
                        <CheckCircle2 aria-hidden="true" className="size-3.5 translate-y-0.5" />
                      ) : (
                        <XCircle aria-hidden="true" className="size-3.5 translate-y-0.5" />
                      )}
                      {verdict.message}
                    </p>
                  ) : null}
                  {verdict?.note ? (
                    <p className="mt-1 text-[12px] leading-snug text-warn">{verdict.note}</p>
                  ) : null}
                </div>

                <div className="flex shrink-0 items-center gap-1.5">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={testing === forwarder.id}
                    onClick={() => void runTest(forwarder)}
                  >
                    <Send aria-hidden="true" className="size-3.5" />
                    {testing === forwarder.id ? 'Sending…' : 'Test'}
                  </Button>
                  <IconButton label="Edit destination" onClick={() => setEditing(forwarder)}>
                    <Pencil aria-hidden="true" className="size-4" />
                  </IconButton>
                  <IconButton label="Delete destination" onClick={() => void remove(forwarder)}>
                    <Trash2 aria-hidden="true" className="size-4" />
                  </IconButton>
                </div>
              </li>
            )
          })}
        </ul>
      </Panel>

      {editing ? (
        <ForwarderSheet
          forwarder={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void load()
          }}
        />
      ) : null}
    </>
  )
}

function ForwarderSheet({
  forwarder,
  onClose,
  onSaved,
}: {
  forwarder: AuditForwarder | null
  onClose: () => void
  onSaved: () => void
}) {
  const [draft, setDraft] = useState<AuditForwarderInput>(
    forwarder
      ? {
          name: forwarder.name,
          kind: forwarder.kind,
          host: forwarder.host,
          port: forwarder.port,
          protocol: forwarder.protocol,
          facility: forwarder.facility,
          app_name: forwarder.app_name,
          octet_counting: forwarder.octet_counting,
          tls_ca_bundle: forwarder.tls_ca_bundle ?? '',
          tls_insecure_skip_verify: forwarder.tls_insecure_skip_verify,
          enabled: forwarder.enabled,
        }
      : EMPTY_FORWARDER,
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function set<K extends keyof AuditForwarderInput>(key: K, value: AuditForwarderInput[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const payload: AuditForwarderInput = {
        ...draft,
        name: draft.name.trim(),
        host: draft.host.trim(),
        app_name: draft.app_name.trim() || 'kubemg',
        // A CA bundle only means something over TLS, and the server refuses one
        // on a plaintext transport rather than storing a setting that cannot do
        // what its author thinks it does.
        tls_ca_bundle: draft.protocol === 'tls' ? draft.tls_ca_bundle?.trim() : '',
        tls_insecure_skip_verify: draft.protocol === 'tls' && draft.tls_insecure_skip_verify,
      }
      if (forwarder) {
        await updateAuditForwarder(forwarder.id, payload)
      } else {
        await createAuditForwarder(payload)
      }
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the destination.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      width="lg"
      eyebrow="Audit forwarding"
      title={forwarder ? `Edit ${forwarder.name}` : 'New audit destination'}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Save destination'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Notice tone="info">
          Every audit record is sent here, including reads — the verb selection that narrows the
          audit table does not narrow this. Records name users, clusters and namespaces, so a
          destination is a data-egress decision.
        </Notice>

        <Field label="Name" htmlFor="forwarder_name" hint="What you recognise this destination by.">
          <TextInput
            id="forwarder_name"
            value={draft.name}
            onChange={(event) => set('name', event.target.value)}
            placeholder="logsign"
          />
        </Field>

        <Field
          label="Transport"
          htmlFor="forwarder_protocol"
          hint={PROTOCOL_HINT[draft.protocol]}
        >
          <Select
            id="forwarder_protocol"
            value={draft.protocol}
            onChange={(event) => {
              const protocol = event.target.value as AuditForwarderProtocol
              setDraft((current) => ({
                ...current,
                protocol,
                // The default port differs between TCP and UDP, so switching
                // transport clears an untouched port back to "ask the server".
                port: current.port === 0 || current.port === 514 || current.port === 515
                  ? 0
                  : current.port,
              }))
            }}
          >
            {(Object.keys(PROTOCOL_LABEL) as AuditForwarderProtocol[]).map((protocol) => (
              <option key={protocol} value={protocol}>
                {PROTOCOL_LABEL[protocol]}
              </option>
            ))}
          </Select>
        </Field>

        <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
          <Field
            label="Host"
            htmlFor="forwarder_host"
            hint="A hostname or IP on its own — no scheme, and the port is the next field. Dialled from kubemg, so it has to be reachable from here."
          >
            <TextInput
              id="forwarder_host"
              className="font-mono text-[12.5px]"
              value={draft.host}
              onChange={(event) => set('host', event.target.value)}
              placeholder="logsign.example.com"
            />
          </Field>
          <Field
            label="Port"
            htmlFor="forwarder_port"
            hint={draft.protocol === 'udp' ? 'Blank uses 514.' : 'Blank uses 515.'}
          >
            <TextInput
              id="forwarder_port"
              inputMode="numeric"
              value={draft.port === 0 ? '' : String(draft.port)}
              onChange={(event) => {
                const digits = event.target.value.replace(/[^0-9]/g, '')
                set('port', digits === '' ? 0 : Number(digits))
              }}
              placeholder={draft.protocol === 'udp' ? '514' : '515'}
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="App name"
            htmlFor="forwarder_app"
            hint="The RFC 5424 APP-NAME field, which is what a SIEM rule filters this stream on."
          >
            <TextInput
              id="forwarder_app"
              className="font-mono text-[12.5px]"
              value={draft.app_name}
              onChange={(event) => set('app_name', event.target.value)}
              placeholder="kubemg"
            />
          </Field>
          <Field
            label="Facility"
            htmlFor="forwarder_facility"
            hint="local0–local7 are the range reserved for applications."
          >
            <Select
              id="forwarder_facility"
              value={String(draft.facility)}
              onChange={(event) => set('facility', Number(event.target.value))}
            >
              {[16, 17, 18, 19, 20, 21, 22, 23].map((facility) => (
                <option key={facility} value={facility}>
                  local{facility - 16} ({facility})
                </option>
              ))}
            </Select>
          </Field>
        </div>

        {draft.protocol === 'udp' ? null : (
          <label className="flex items-start gap-2.5">
            <input
              type="checkbox"
              className="mt-0.5 size-4 accent-[var(--color-accent)]"
              checked={draft.octet_counting}
              onChange={(event) => set('octet_counting', event.target.checked)}
            />
            <span className="min-w-0">
              <span className="text-[13px] text-fg">Frame with a length prefix (RFC 6587)</span>
              <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                A stream has no message boundary of its own, so records are separated either by a
                trailing newline (the default, and what most collectors expect) or by a byte count
                in front of each one. Set this only if your collector is configured for octet
                counting — a receiver expecting one and given the other concatenates every record
                into a single unparseable line.
              </span>
            </span>
          </label>
        )}

        {draft.protocol === 'tls' ? (
          <>
            <Field
              label="CA bundle"
              htmlFor="forwarder_ca"
              hint="PEM. Leave blank for a publicly-issued certificate; paste the issuing CA for the private one most SIEM appliances use."
            >
              <TextArea
                id="forwarder_ca"
                rows={5}
                className="font-mono text-[12px]"
                value={draft.tls_ca_bundle ?? ''}
                onChange={(event) => set('tls_ca_bundle', event.target.value)}
                placeholder="-----BEGIN CERTIFICATE-----"
              />
            </Field>
            <label className="flex items-start gap-2.5">
              <input
                type="checkbox"
                className="mt-0.5 size-4 accent-[var(--color-accent)]"
                checked={draft.tls_insecure_skip_verify}
                onChange={(event) => set('tls_insecure_skip_verify', event.target.checked)}
              />
              <span className="min-w-0">
                <span className="text-[13px] text-fg">Do not verify the collector’s certificate</span>
                <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                  The connection is still encrypted, but nothing proves who is on the other end of
                  it — and what travels down it is the audit trail. Use this only for an appliance
                  with a self-signed certificate you cannot export; pasting its CA above is the
                  better answer wherever it is possible.
                </span>
              </span>
            </label>
          </>
        ) : null}

        <label className="flex items-start gap-2.5 border-t border-line-soft pt-4">
          <input
            type="checkbox"
            className="mt-0.5 size-4 accent-[var(--color-accent)]"
            checked={draft.enabled}
            onChange={(event) => set('enabled', event.target.checked)}
          />
          <span className="min-w-0">
            <span className="text-[13px] text-fg">Send records to this destination</span>
            <span className="mt-0.5 block text-[12px] leading-snug text-muted">
              Turning it off keeps the configuration and stops the delivery. Records are not queued
              while it is off — they go to the table and this server’s log as always.
            </span>
          </span>
        </label>
      </div>
    </Sheet>
  )
}
