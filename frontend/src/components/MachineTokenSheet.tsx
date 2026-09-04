import { useState } from 'react'
import type { FormEvent } from 'react'
import { Check, Copy, Download } from 'lucide-react'
import { errorMessage, issueMachineToken } from '../api/client'
import type { Cluster, IssuedMachineToken, MachineAccount } from '../api/types'
import { formatTTL } from '../lib/time'
import { Button, Field, Notice, Segmented, Sheet, TextInput } from './primitives'
import { YamlView } from './YamlView'

/**
 * The windows worth offering for a credential a system holds rather than a
 * person. It starts a day out and ends past a year, where the human ladder in
 * KubeconfigDrawer stops at a quarter: a release pipeline is re-credentialled by
 * somebody remembering to, and an eight-hour token would mean remembering daily.
 *
 * `0` is "never", and it is last rather than absent. Allowing it is a decision
 * with a cost — the only thing that then closes the credential is somebody
 * revoking it — so it is offered plainly and disclosed on the way out, rather
 * than being something an operator works around by asking for ten years.
 */
const TTL_LADDER = [86400, 7 * 86400, 30 * 86400, 90 * 86400, 365 * 86400, 0]

/** IssueMachineTokenSheet mints one credential and shows it once. */
export function IssueMachineTokenSheet({
  account,
  clusters,
  onClose,
  onIssued,
}: {
  account: MachineAccount
  clusters: Cluster[]
  onClose: () => void
  onIssued: () => Promise<void>
}) {
  // Only a cluster the account already has a grant on can be issued against —
  // the server refuses the rest, and offering them would be offering a refusal.
  const eligible = clusters.filter((cluster) =>
    account.access.some((grant) => grant.cluster_id === cluster.id),
  )
  const [name, setName] = useState('')
  const [clusterId, setClusterId] = useState(String(eligible[0]?.id ?? ''))
  const [ttl, setTtl] = useState('7776000')
  const [namespace, setNamespace] = useState('')
  const [issued, setIssued] = useState<IssuedMachineToken | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const cluster = eligible.find((entry) => String(entry.id) === clusterId)
  const grant = account.access.find((entry) => String(entry.cluster_id) === clusterId)
  const direct = cluster?.connection_mode === 'direct'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const next = await issueMachineToken(account.id, {
        name: name.trim(),
        cluster_id: Number(clusterId),
        namespace: namespace.trim() || undefined,
        ...(ttl === '0' ? { never_expires: true } : { ttl_seconds: Number(ttl) }),
      })
      setIssued(next)
      await onIssued()
    } catch (err) {
      setIssued(null)
      setError(errorMessage(err, 'Could not issue the credential.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Programmatic access"
      width="lg"
      title={
        <>
          Credential for <span className="font-mono text-accent">{account.username}</span>
        </>
      }
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || name.trim() === '' || clusterId === '' || direct}
          >
            {busy ? 'Issuing…' : issued ? 'Issue another' : 'Issue credential'}
          </Button>
        </>
      }
    >
      {eligible.length === 0 ? (
        <Notice tone="warn">
          This account has no cluster access yet. Grant it a role first — a credential issued now
          would authenticate and then be refused by the cluster.
        </Notice>
      ) : null}

      <Field
        label="What holds it"
        htmlFor="token-name"
        hint="The system this credential is going into. It is what you will read when deciding which one to revoke."
      >
        <TextInput
          id="token-name"
          placeholder="jenkins release pipeline"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>

      <Field label="Cluster" htmlFor="token-cluster">
        <select
          id="token-cluster"
          className="control w-full"
          value={clusterId}
          onChange={(event) => setClusterId(event.target.value)}
        >
          {eligible.map((entry) => (
            <option key={entry.id} value={entry.id}>
              {entry.name}
            </option>
          ))}
        </select>
      </Field>

      {/* Direct mode is refused by the server rather than served, and the reason
          is worth stating before the click: there the credential is minted on the
          cluster itself, so kubemg could not revoke what it issued. */}
      {direct ? (
        <Notice tone="warn">
          {cluster?.name} is registered in direct mode. Programmatic access needs an agent tunnel —
          in direct mode the credential is minted on the cluster, so kubemg cannot revoke it and the
          cluster’s RBAC has nothing bound to it.
        </Notice>
      ) : null}

      <div className="flex flex-col gap-1.5">
        <span className="label">Valid for</span>
        <Segmented
          ariaLabel="Valid for"
          value={ttl}
          onChange={setTtl}
          options={TTL_LADDER.map((seconds) => ({
            value: String(seconds),
            label: seconds === 0 ? 'Never expires' : formatTTL(seconds),
          }))}
        />
        {ttl === '0' ? (
          <p className="text-[12px] text-warn">
            Nothing closes this credential except revoking it here or disabling the account. Review
            it against its last-used time — that column is what replaces the clock.
          </p>
        ) : null}
      </div>

      <Field
        label="Namespace"
        htmlFor="token-namespace"
        hint={
          grant && grant.namespaces.length > 0
            ? `This account’s grant covers ${grant.namespaces.join(', ')}.`
            : 'The kubeconfig’s default context namespace. It is a convenience — the boundary is the account’s grant.'
        }
      >
        <TextInput
          id="token-namespace"
          className="font-mono"
          placeholder={grant?.namespaces[0] ?? 'default'}
          value={namespace}
          onChange={(event) => setNamespace(event.target.value)}
        />
      </Field>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {issued ? <IssuedCredential issued={issued} account={account} /> : null}
    </Sheet>
  )
}

function IssuedCredential({
  issued,
  account,
}: {
  issued: IssuedMachineToken
  account: MachineAccount
}) {
  const [copied, setCopied] = useState<'secret' | 'kubeconfig' | null>(null)
  const [copyError, setCopyError] = useState<string | null>(null)

  async function copy(what: 'secret' | 'kubeconfig') {
    try {
      await navigator.clipboard.writeText(what === 'secret' ? issued.secret : issued.kubeconfig)
      setCopyError(null)
      setCopied(what)
      window.setTimeout(() => setCopied(null), 2000)
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
      {/* Said before the value, not after: what is stored is a hash, so this
          screen is the only place the secret will ever exist. */}
      <Notice tone="warn">
        This is the only time the credential is shown. kubemg stores a hash of it — if it is lost,
        revoke this one and issue another.
      </Notice>

      {issued.warning ? <Notice tone="warn">{issued.warning}</Notice> : null}

      <div className="flex flex-col gap-1.5">
        <span className="label">Token</span>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-control border border-line bg-raised/50 px-3 py-2 font-mono text-[12.5px] text-fg">
            {issued.secret}
          </code>
          <Button type="button" variant="ghost" onClick={() => copy('secret')}>
            {copied === 'secret' ? (
              <Check aria-hidden="true" className="size-4" />
            ) : (
              <Copy aria-hidden="true" className="size-4" />
            )}
            Copy
          </Button>
        </div>
        <p className="text-[12px] text-muted">
          A CI job can use this as a bearer token against{' '}
          <span className="font-mono">{issued.server}</span>, but the kubeconfig below is the form
          kubectl and most tooling expect.
        </p>
      </div>

      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <span className="label">Kubeconfig</span>
          <span className="ml-auto flex gap-2">
            <Button type="button" variant="ghost" onClick={() => copy('kubeconfig')}>
              {copied === 'kubeconfig' ? (
                <Check aria-hidden="true" className="size-4" />
              ) : (
                <Copy aria-hidden="true" className="size-4" />
              )}
              Copy
            </Button>
            <Button type="button" variant="ghost" onClick={download}>
              <Download aria-hidden="true" className="size-4" />
              Download
            </Button>
          </span>
        </div>
        <YamlView value={issued.kubeconfig} numbered={false} />
      </div>

      {/*
        What to do with the file, because a kubeconfig on screen is not yet
        access: the two lines below are the whole of it, and having them here
        is what makes this sheet the end of the task rather than the start of a
        search through the docs.
      */}
      <div className="flex flex-col gap-1.5">
        <span className="label">Use it</span>
        <YamlView
          value={[
            `# save the file above as ${issued.filename}, then:`,
            `export KUBECONFIG=$PWD/${issued.filename}`,
            `kubectl --context ${issued.context} get pods`,
          ].join('\n')}
          numbered={false}
        />
        <p className="text-[12px] text-muted">
          It acts as <span className="font-mono">{account.username}</span> with the{' '}
          <span className="font-mono">{issued.k8s_role}</span> role, and every call it makes is in
          the audit trail under that name. In a pipeline, keep the file in the runner&rsquo;s own
          secret store — kubemg cannot hand it out a second time.
        </p>
      </div>

      {copyError ? <Notice tone="error">{copyError}</Notice> : null}
    </div>
  )
}
