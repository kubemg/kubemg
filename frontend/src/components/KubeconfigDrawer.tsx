import { useState } from 'react'
import type { FormEvent } from 'react'
import { Check, Copy, Download } from 'lucide-react'
import { errorMessage, generateKubeconfig } from '../api/client'
import type { Cluster, Kubeconfig } from '../api/types'
import { formatDuration, useCountdown } from '../lib/time'
import {
  Button,
  DetailList,
  Field,
  Notice,
  Segmented,
  Slab,
  Sheet,
  TextInput,
} from './primitives'

const TTL_CHOICES = [
  { value: '3600', label: '1 hour' },
  { value: '28800', label: '8 hours' },
  { value: '86400', label: '24 hours' },
]

/**
 * KubeconfigDrawer issues access to one cluster. It is a cluster action, not a
 * destination: it always opens over the cluster it applies to.
 */
export function KubeconfigDrawer({
  cluster,
  onClose,
}: {
  cluster: Cluster
  onClose: () => void
}) {
  const [ttl, setTtl] = useState('3600')
  const [namespace, setNamespace] = useState('')
  const [issued, setIssued] = useState<Kubeconfig | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      setIssued(await generateKubeconfig(cluster.id, Number(ttl), namespace.trim()))
    } catch (err) {
      setIssued(null)
      setError(errorMessage(err, 'Could not generate a kubeconfig.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Cluster access"
      title={
        <>
          Kubeconfig for <span className="font-mono text-accent">{cluster.name}</span>
        </>
      }
      onClose={onClose}
      onSubmit={handleSubmit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Generating…' : issued ? 'Generate again' : 'Generate kubeconfig'}
          </Button>
        </>
      }
    >
      {/* The two connection modes issue two different credentials, and the
          difference matters to whoever uses the file: one authenticates
          straight at the API server, the other only ever reaches KubeMG. */}
      <Notice tone="info">
        {cluster.connection_mode === 'agent'
          ? 'kubectl will talk to KubeMG, which replays each call down this cluster’s agent ' +
            'tunnel as you. The cluster’s own RBAC decides what you may do, and every call is audited.'
          : 'kubectl will talk to this cluster’s API server directly, using a short-lived token ' +
            'minted on the cluster.'}
      </Notice>

      <div className="flex flex-col gap-1.5">
        <span className="label">Valid for</span>
        <Segmented
          ariaLabel="Valid for"
          value={ttl}
          onChange={setTtl}
          options={TTL_CHOICES.map((choice) => ({ value: choice.value, label: choice.label }))}
        />
      </div>

      <Field
        label="Namespace"
        htmlFor="namespace"
        hint={
          cluster.namespaces.length > 0
            ? `Your grant covers ${cluster.namespaces.join(', ')}.`
            : 'Defaults to "default".'
        }
      >
        <TextInput
          id="namespace"
          name="namespace"
          list="granted-namespaces"
          className="font-mono"
          placeholder={cluster.namespaces[0] ?? 'default'}
          value={namespace}
          onChange={(event) => setNamespace(event.target.value)}
        />
        <datalist id="granted-namespaces">
          {cluster.namespaces.map((name) => (
            <option key={name} value={name} />
          ))}
        </datalist>
      </Field>

      {error ? <Notice tone="error">{error}</Notice> : null}

      {issued ? <IssuedCredential issued={issued} /> : null}
    </Sheet>
  )
}

function IssuedCredential({ issued }: { issued: Kubeconfig }) {
  const remaining = useCountdown(issued.expires_at)
  const [copied, setCopied] = useState(false)
  const [copyError, setCopyError] = useState<string | null>(null)

  const expired = remaining === 0
  const fraction = Math.min(1, Math.max(0, remaining / issued.ttl_seconds))
  const low = !expired && fraction <= 0.25

  const tone = expired ? 'text-danger' : low ? 'text-warn' : 'text-fg'
  const track = expired ? 'bg-danger' : low ? 'bg-warn' : 'bg-ok'

  async function copy() {
    try {
      await navigator.clipboard.writeText(issued.kubeconfig)
      setCopyError(null)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      setCopyError('Clipboard access was blocked. Download the file instead.')
    }
  }

  function download() {
    const blob = new Blob([issued.kubeconfig], { type: 'application/yaml' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = issued.filename
    link.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="rise flex flex-col gap-4 border-t border-line-soft pt-4" aria-live="polite">
      {/* The credential is deliberately short-lived, so the sheet shows it
          running out rather than only saying when it expires. */}
      <div className="rounded-card border border-line bg-raised/50 p-4">
        <div className="flex items-end justify-between gap-4">
          <div>
            <p className="label">Time left</p>
            <p className={`mt-1 font-mono text-[32px] leading-none font-semibold ${tone}`}>
              {formatDuration(remaining)}
            </p>
          </div>
          <p className="text-right text-[11.5px] text-muted">
            expires {new Date(issued.expires_at).toLocaleTimeString()}
          </p>
        </div>

        <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-line">
          <div
            className={`h-full rounded-full transition-[width] duration-1000 ease-linear ${track}`}
            style={{ width: `${fraction * 100}%` }}
          />
        </div>

        {expired ? (
          <p className="mt-3 text-[12.5px] text-muted">
            This token no longer authenticates. Generate a new one to keep working.
          </p>
        ) : null}
      </div>

      {issued.warning ? <Notice tone="warn">{issued.warning}</Notice> : null}

      <DetailList
        columns={2}
        rows={[
          { term: 'Context', value: issued.context },
          { term: 'Namespace', value: issued.namespace },
          // Only direct mode authenticates as a service account; the bastion
          // impersonates the caller, so naming one there would be a lie.
          issued.connection_mode === 'agent'
            ? { term: 'Server', value: issued.server }
            : { term: 'Service account', value: issued.service_account },
          { term: 'Role', value: issued.k8s_role },
        ]}
      />

      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" onClick={copy}>
          {copied ? (
            <Check aria-hidden="true" className="size-4 text-ok" />
          ) : (
            <Copy aria-hidden="true" className="size-4" />
          )}
          {copied ? 'Copied' : 'Copy'}
        </Button>
        <Button type="button" onClick={download}>
          <Download aria-hidden="true" className="size-4" />
          Download {issued.filename}
        </Button>
      </div>

      {copyError ? <Notice tone="error">{copyError}</Notice> : null}

      <Slab className="max-h-64">{issued.kubeconfig}</Slab>
    </div>
  )
}
