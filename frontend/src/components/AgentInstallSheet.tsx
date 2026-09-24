import { useCallback, useEffect, useState } from 'react'
import { AlertTriangle, Download } from 'lucide-react'
import { errorMessage, fetchAgentInstall } from '../api/client'
import type { AgentInstall, Cluster } from '../api/types'
import { downloadLinkState, saveManifest } from '../lib/agentInstall'
import { Button, CodeBlock, DetailList, Notice, Sheet } from './primitives'
import { CardSkeleton } from './SkeletonLoader'
import { YamlView } from './YamlView'

/**
 * The install package, as content rather than as a surface.
 *
 * Registration renders it inside the wizard's own Panel; a cluster that already
 * exists renders it in a Sheet. It is the same handoff either way — one command
 * to run somewhere else, the token shown separately and masked because it is the
 * cluster's only credential — so it is written once and framed twice.
 *
 * The URL in the commands is a single-use download, not the credential: it is
 * minted when this is rendered, spent by the first fetch, and dies unused at its
 * expiry. That is said under the command, because a URL that worked once and
 * then answers 404 is otherwise a mystery.
 */
export function AgentInstallBody({
  install,
  onRenew,
}: {
  install: AgentInstall
  /** Mints a fresh download URL: for one that expired, or was spent by an apply that failed. */
  onRenew?: () => void
}) {
  const [showManifest, setShowManifest] = useState(false)
  const link = downloadLinkState(install.download_expires_at, Date.now())

  return (
    <>
      <CodeBlock label="Install command" value={install.apply_command} />
      <p
        className={`flex flex-wrap items-baseline gap-x-2 text-[12px] leading-snug ${link.expired ? 'text-warn' : 'text-muted'}`}
      >
        <span>{link.note}</span>
        {onRenew ? (
          <button
            type="button"
            onClick={onRenew}
            className="text-accent transition-colors hover:underline"
          >
            New URL
          </button>
        ) : null}
      </p>

      <details className="group">
        <summary className="cursor-pointer text-[12.5px] text-muted transition-colors hover:text-fg">
          Prefer Kustomize?
        </summary>
        <div className="mt-2.5 flex flex-col gap-2">
          <p className="text-[12px] leading-snug text-muted">
            Kustomize only accepts local paths and Git specs as remote targets, so the package is
            fetched and extracted first.
          </p>
          <CodeBlock value={install.kustomize_command} />
        </div>
      </details>

      <div className="border-t border-line-soft pt-4">
        <DetailList
          columns={2}
          rows={[
            { term: 'Bastion', value: install.bastion_url },
            { term: 'Namespace', value: install.namespace },
            { term: 'Image', value: install.image },
          ]}
        />
      </div>

      <CodeBlock label="Registration token" value={install.agent_token} secret />
      <p className="flex items-start gap-1.5 text-[12px] leading-snug text-warn">
        <AlertTriangle aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
        This token authenticates the tunnel for this cluster. It is not in the URL above; it is inside
        the package that URL downloads — treat the package like a credential.
      </p>

      <div>
        <button
          type="button"
          onClick={() => setShowManifest((current) => !current)}
          className="text-[12.5px] text-muted transition-colors hover:text-fg"
        >
          {showManifest ? 'Hide' : 'Review'} the manifest before applying
        </button>
        {/* Reviewing a manifest before applying it to a cluster is reading, so it
            gets the same painted surface the object editor does. */}
        {showManifest ? <YamlView value={install.manifest} className="mt-2.5 h-80" /> : null}
      </div>
    </>
  )
}

/**
 * AgentInstallSheet re-issues the install package for a cluster that is already
 * registered.
 *
 * An agent is upgraded by applying the manifests again, and until now the only
 * place they existed was step three of a wizard nobody can walk back into — so
 * the commands were a thing an operator had to have kept. The package is
 * rendered from the cluster's stored registration token against the *current*
 * settings, so re-reading it here is how an operator picks up a changed agent
 * image or a corrected public URL. Opening this mints a single-use download URL
 * and nothing else: the tunnel credential is never rotated by opening it —
 * that is its own, confirmed action on the dashboard.
 *
 * `rotated` is the package a rotation just answered with: it is shown as-is
 * rather than fetched again, beside what the rotation did.
 */
export function AgentInstallSheet({
  cluster,
  onClose,
  rotated,
}: {
  cluster: Cluster
  onClose: () => void
  rotated?: AgentInstall
}) {
  const [install, setInstall] = useState<AgentInstall | null>(rotated ?? null)
  const [error, setError] = useState<string | null>(null)

  const renew = useCallback(() => {
    let live = true
    fetchAgentInstall(cluster.id)
      .then((data) => {
        if (live) {
          setInstall(data)
          setError(null)
        }
      })
      .catch((err) => {
        if (live) setError(errorMessage(err, 'Could not render the install package.'))
      })
    return () => {
      live = false
    }
  }, [cluster.id])

  useEffect(() => {
    if (rotated) return
    return renew()
  }, [renew, rotated])

  return (
    <Sheet
      eyebrow="Agent"
      title={`Install or upgrade the agent on ${cluster.name}`}
      onClose={onClose}
      width="lg"
      footer={
        <>
          {install ? (
            /* Saved from the manifest already on this page, never fetched from
               the install URL — a browser download would spend the single-use
               ticket the command above still needs. */
            <button
              type="button"
              onClick={() => saveManifest(install)}
              className="inline-flex h-9 items-center gap-2 rounded-control border border-line px-3 text-[13px] text-muted transition-colors hover:text-fg"
            >
              <Download aria-hidden="true" className="size-4" />
              Download YAML
            </button>
          ) : null}
          <Button onClick={onClose}>Close</Button>
        </>
      }
    >
      {rotated ? (
        <Notice tone="warn">
          The registration token was rotated. The old one is refused and the agent stays disconnected
          until this package is applied to {cluster.name}.
        </Notice>
      ) : null}
      <p className="text-[13px] leading-relaxed text-muted">
        Run this against {cluster.name} with a kubeconfig that can create resources in{' '}
        {install?.namespace ?? 'the agent namespace'}. Applying it again is also how the agent is
        upgraded — the manifests are rendered from this cluster's current registration token, so
        re-applying them replaces the workload without re-registering the cluster or invalidating
        anything already issued.
      </p>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {!install && !error ? <CardSkeleton lines={4} label="Rendering the install package" /> : null}
      {install ? <AgentInstallBody install={install} onRenew={renew} /> : null}
    </Sheet>
  )
}

