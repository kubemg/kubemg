import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Check, Copy, Download, X } from 'lucide-react'
import { errorMessage, generateKubeconfig } from '../api/client'
import type { Cluster, Kubeconfig } from '../api/types'
import { formatDuration, useCountdown } from '../lib/time'
import { Button, Field, Notice, TextInput } from './primitives'

const TTL_CHOICES = [
  { seconds: 3600, label: '1h' },
  { seconds: 28800, label: '8h' },
  { seconds: 86400, label: '24h' },
]

/** Segments in the validity gauge. Each one is a fixed slice of the TTL. */
const GAUGE_SEGMENTS = 24

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
  const [ttlSeconds, setTtlSeconds] = useState(3600)
  const [namespace, setNamespace] = useState('')
  const [issued, setIssued] = useState<Kubeconfig | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      setIssued(await generateKubeconfig(cluster.id, ttlSeconds, namespace.trim()))
    } catch (err) {
      setIssued(null)
      setError(errorMessage(err, 'Could not generate a kubeconfig.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-20 flex justify-end">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="absolute inset-0 bg-ink/45"
      />

      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="kubeconfig-title"
        className="drawer-in relative flex h-full w-full max-w-[440px] flex-col border-l border-line bg-surface"
      >
        <header className="flex h-12 shrink-0 items-center justify-between border-b border-line px-4">
          <h2 id="kubeconfig-title" className="min-w-0 truncate text-[14px] font-semibold text-fg">
            Access <span className="font-mono text-primary">{cluster.name}</span>
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="rounded-[5px] p-1 text-muted transition-colors hover:bg-raised hover:text-fg"
          >
            <X aria-hidden="true" className="size-4" />
            <span className="sr-only">Close</span>
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <form onSubmit={handleSubmit} className="flex flex-col gap-3.5 border-b border-line p-3">
            <fieldset>
              <legend className="label mb-1">Valid for</legend>
              <div className="grid grid-cols-3 gap-1">
                {TTL_CHOICES.map((choice) => {
                  const active = choice.seconds === ttlSeconds
                  return (
                    <button
                      key={choice.seconds}
                      type="button"
                      aria-pressed={active}
                      onClick={() => setTtlSeconds(choice.seconds)}
                      className={`rounded-sm border py-1.5 font-mono text-[12px] transition-colors ${
                        active
                          ? 'border-primary/40 bg-primary-soft font-medium text-primary'
                          : 'border-line bg-surface text-muted hover:border-faint hover:text-fg'
                      }`}
                    >
                      {choice.label}
                    </button>
                  )
                })}
              </div>
            </fieldset>

            <Field
              label="Namespace"
              htmlFor="namespace"
              hint={
                cluster.namespaces.length > 0
                  ? `Your access covers ${cluster.namespaces.join(', ')}.`
                  : 'Defaults to "default".'
              }
            >
              <TextInput
                id="namespace"
                name="namespace"
                list="granted-namespaces"
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

            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Generating…' : issued ? 'Generate again' : 'Generate kubeconfig'}
            </Button>
          </form>

          {issued ? <IssuedCredential issued={issued} /> : null}
        </div>
      </div>
    </div>
  )
}

/**
 * ValidityGauge is the read-out for a draining credential: discrete segments so
 * the remaining time can be read at a glance, each worth a fixed slice of the
 * requested TTL.
 */
function ValidityGauge({ fraction, tone }: { fraction: number; tone: string }) {
  const filled = Math.ceil(fraction * GAUGE_SEGMENTS)

  return (
    <div className="flex h-2.5 gap-px" aria-hidden="true">
      {Array.from({ length: GAUGE_SEGMENTS }, (_, index) => (
        <span
          key={index}
          className={`flex-1 rounded-[1px] transition-colors ${index < filled ? tone : 'bg-line'}`}
        />
      ))}
    </div>
  )
}

function segmentSize(ttlSeconds: number): string {
  const minutes = Math.round(ttlSeconds / 60 / GAUGE_SEGMENTS)
  return minutes >= 60 ? `${Math.round(minutes / 60)}h` : `${minutes} min`
}

function IssuedCredential({ issued }: { issued: Kubeconfig }) {
  const remaining = useCountdown(issued.expires_at)
  const [copied, setCopied] = useState(false)
  const [copyError, setCopyError] = useState<string | null>(null)

  const expired = remaining === 0
  const fraction = Math.min(1, Math.max(0, remaining / issued.ttl_seconds))
  const low = !expired && fraction <= 0.25

  const tone = expired ? 'text-danger' : low ? 'text-warn' : 'text-fg'
  const gaugeTone = expired ? 'bg-danger' : low ? 'bg-warn' : 'bg-primary'

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

  const rows: Array<[string, string]> = [
    ['Context', issued.context],
    ['Namespace', issued.namespace],
    ['Service account', issued.service_account],
    ['Role', issued.k8s_role],
  ]

  return (
    <div className="enter" aria-live="polite">
      <div className="border-b border-line p-3">
        <div className="flex items-end justify-between gap-4">
          <div>
            <p className="label">Time left</p>
            <p className={`mt-0.5 font-mono text-3xl font-semibold tabular-nums ${tone}`}>
              {formatDuration(remaining)}
            </p>
          </div>
          <p className="text-right text-[11px] text-muted">
            each segment {segmentSize(issued.ttl_seconds)}
          </p>
        </div>

        <div className="mt-2.5">
          <ValidityGauge fraction={fraction} tone={gaugeTone} />
        </div>

        {expired ? (
          <p className="mt-2.5 text-[12px] text-muted">
            This token no longer authenticates. Generate a new one to keep working.
          </p>
        ) : null}
      </div>

      <dl className="grid grid-cols-[110px_minmax(0,1fr)] gap-x-3 gap-y-1.5 border-b border-line p-3 text-[12px]">
        {rows.map(([term, value]) => (
          <div key={term} className="contents">
            <dt className="text-muted">{term}</dt>
            <dd className="truncate font-mono text-fg" title={value}>
              {value}
            </dd>
          </div>
        ))}
      </dl>

      <div className="flex flex-wrap items-center gap-2 border-b border-line p-3">
        <Button type="button" onClick={copy}>
          {copied ? (
            <Check aria-hidden="true" className="size-3.5 text-ok" />
          ) : (
            <Copy aria-hidden="true" className="size-3.5" />
          )}
          {copied ? 'Copied' : 'Copy'}
        </Button>
        <Button type="button" onClick={download}>
          <Download aria-hidden="true" className="size-3.5" />
          Download
        </Button>
      </div>

      {copyError ? (
        <div className="p-3">
          <Notice tone="error">{copyError}</Notice>
        </div>
      ) : null}

      <div className="px-3 pb-3">
        <p className="label pt-3 pb-1.5">{issued.filename}</p>
        <pre className="max-h-64 overflow-auto rounded-panel bg-ink p-3 font-mono text-[11.5px] leading-relaxed text-ink-fg">
          {issued.kubeconfig}
        </pre>
      </div>
    </div>
  )
}
