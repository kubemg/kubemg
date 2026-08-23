import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Check, Copy, Download } from 'lucide-react'
import { errorMessage, fetchKubeconfigPolicy, generateKubeconfig } from '../api/client'
import type { Cluster, Kubeconfig, KubeconfigPolicy } from '../api/types'
import { formatDuration, formatTTL, useCountdown } from '../lib/time'
import {
  Button,
  DetailList,
  Field,
  Notice,
  Segmented,
  Sheet,
  TextInput,
} from './primitives'
import { YamlView } from './YamlView'

/**
 * The windows worth offering, from a shift to a quarter. Which of them a caller
 * may actually pick is the server's decision — an administrator moves the
 * ceiling — so the ladder is filtered against the policy rather than being the
 * choice itself.
 *
 * It stays a ladder of presets rather than a number box for the reason the JIT
 * window does: a text field invites 480, and nobody typing into one is choosing
 * between two windows they can see side by side.
 */
const TTL_LADDER = [3600, 8 * 3600, 86400, 7 * 86400, 30 * 86400, 90 * 86400]

/** ttlChoices is the ladder narrowed to what this install allows, with the
    ceiling itself always offered — an admin who sets 36 hours means it to be
    reachable, not rounded down to a day. */
function ttlChoices(policy: KubeconfigPolicy | null): number[] {
  const max = policy?.max_ttl_seconds ?? 86400
  const min = policy?.min_ttl_seconds ?? 600
  const rungs = TTL_LADDER.filter((seconds) => seconds >= min && seconds <= max)
  return rungs.includes(max) ? rungs : [...rungs, max]
}

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
  const [policy, setPolicy] = useState<KubeconfigPolicy | null>(null)
  const [ttl, setTtl] = useState('3600')
  const [namespace, setNamespace] = useState('')
  const [issued, setIssued] = useState<Kubeconfig | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // How long a credential may live is an install-wide policy, so the sheet asks
  // rather than assuming. A failure leaves the default ladder in place: the
  // server refuses anything past its own ceiling anyway, so the worst case is a
  // refusal carrying the real number rather than a form that cannot be used.
  useEffect(() => {
    let live = true
    void fetchKubeconfigPolicy()
      .then((next) => {
        if (live) setPolicy(next)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [])

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
          options={ttlChoices(policy).map((seconds) => ({
            value: String(seconds),
            label: formatTTL(seconds),
          }))}
        />
        {/* A window past a day is worth a word: the file outlives the session
            that generated it, and in direct mode it outlives the grant too —
            the token is the cluster's, and revoking access here does not reach
            it. */}
        {Number(ttl) > 86400 ? (
          <p className="text-[12px] text-muted">
            {cluster.connection_mode === 'agent'
              ? 'A long-lived file, but not long-lived access: every call re-reads your grant, so revoking it stops this kubeconfig at once.'
              : 'This token is minted on the cluster and cannot be withdrawn before it expires — revoking access in KubeMG does not reach it. Keep the file somewhere a laptop backup will not.'}
          </p>
        ) : null}
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
          {/* A time of day is enough for a credential that dies this afternoon
              and useless for one that dies in July, so the date joins it once
              the window is longer than a day. */}
          <p className="text-right text-[11.5px] text-muted">
            expires{' '}
            {issued.ttl_seconds > 86400
              ? new Date(issued.expires_at).toLocaleString()
              : new Date(issued.expires_at).toLocaleTimeString()}
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

      {/* A kubeconfig is read to check one field — which server, which user —
          before it is saved, so it gets the manifest surface rather than a plain
          slab. Unnumbered: nothing here ever answers with a line number. */}
      <YamlView value={issued.kubeconfig} numbered={false} className="h-64 shrink-0" />
    </div>
  )
}
