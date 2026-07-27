import { useCallback, useEffect, useState } from 'react'
import { Check, Copy, Pencil, RefreshCw, Undo2 } from 'lucide-react'
import { errorMessage, fetchResourceYaml, updateResourceYaml } from '../api/client'
import type { Cluster, ResourceManifest } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { Button, Notice, Pill, Sheet, Spinner } from './primitives'
import { YamlView } from './YamlView'

/** ManifestTarget is one object addressed the way the sidebar addresses it. */
export interface ManifestTarget {
  kind: ResourceKey
  /** The singular label for this kind, for the drawer's eyebrow. */
  label: string
  name: string
  namespace?: string
  /** Whether the drawer opens ready to type. */
  editing?: boolean
}

/**
 * YamlDrawer is the whole object, rather than the handful of columns a list
 * shows. It reads through the same tunnel as every other call and writes back
 * through it too, impersonated — so what a caller may change is decided by the
 * target cluster's RBAC, in the cluster's own words, and lands in the audit
 * trail as an `update` on the object it touched.
 */
export function YamlDrawer({
  cluster,
  target,
  onClose,
  onApplied,
}: {
  cluster: Cluster
  target: ManifestTarget
  onClose: () => void
  onApplied?: () => Promise<void> | void
}) {
  const [manifest, setManifest] = useState<ResourceManifest | null>(null)
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(target.editing ?? false)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applied, setApplied] = useState(false)
  const [copied, setCopied] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await fetchResourceYaml(cluster.id, target.kind, target.name, target.namespace)
      setManifest(result)
      setDraft(result.yaml)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read this object from the cluster.'))
      setManifest(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, target.kind, target.name, target.namespace])

  useEffect(() => {
    void load()
  }, [load])

  const dirty = manifest !== null && draft !== manifest.yaml

  // Closing on a half-typed manifest throws the edit away, so it asks first.
  // Escape reaches the same guard, because the Sheet closes through it too.
  const close = useCallback(() => {
    if (dirty && !window.confirm('Discard your unsaved changes to this manifest?')) return
    onClose()
  }, [dirty, onClose])

  async function save() {
    if (!manifest) return

    setSaving(true)
    setApplied(false)
    try {
      const result = await updateResourceYaml(
        cluster.id,
        target.kind,
        target.name,
        target.namespace,
        draft,
      )
      setManifest(result)
      setDraft(result.yaml)
      setEditing(false)
      setApplied(true)
      setError(null)
      await onApplied?.()
    } catch (err) {
      // The cluster's own refusal is the useful one — "deployments.apps is
      // forbidden" says far more than anything invented here would.
      setError(errorMessage(err, 'The cluster did not accept this manifest.'))
    } finally {
      setSaving(false)
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(draft)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard access is denied outside a secure context; the manifest is
      // selectable on screen either way.
    }
  }

  const editable = manifest?.editable ?? false

  return (
    <Sheet
      width="lg"
      eyebrow={`${cluster.name}${target.namespace ? ` · ${target.namespace}` : ''} · ${target.label}`}
      title={<span className="font-mono">{target.name}</span>}
      onClose={close}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={close}>
            Close
          </Button>
          {editing ? (
            <>
              <Button
                type="button"
                onClick={() => setDraft(manifest?.yaml ?? '')}
                disabled={!dirty || saving}
              >
                <Undo2 aria-hidden="true" className="size-4" />
                Revert
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={() => void save()}
                disabled={!dirty || saving}
              >
                {saving ? <Spinner className="size-4" /> : <Check aria-hidden="true" className="size-4" />}
                Apply to cluster
              </Button>
            </>
          ) : (
            <Button
              type="button"
              variant="primary"
              onClick={() => setEditing(true)}
              disabled={!editable || loading}
            >
              <Pencil aria-hidden="true" className="size-4" />
              Edit config
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-wrap items-center gap-2">
        {manifest ? (
          <>
            <Pill tone="idle" dot={false}>
              {manifest.api_version}
            </Pill>
            <Pill tone="idle" dot={false}>
              {manifest.kind}
            </Pill>
            {editing ? (
              <Pill tone={dirty ? 'warn' : 'accent'} dot={false}>
                {dirty ? 'edited' : 'editing'}
              </Pill>
            ) : null}
          </>
        ) : null}

        <div className="ml-auto flex items-center gap-2">
          <Button type="button" size="sm" onClick={() => void copy()} disabled={!manifest}>
            {copied ? (
              <Check aria-hidden="true" className="size-3.5 text-ok" />
            ) : (
              <Copy aria-hidden="true" className="size-3.5" />
            )}
            {copied ? 'Copied' : 'Copy'}
          </Button>
          <Button
            type="button"
            size="sm"
            onClick={() => void load()}
            disabled={loading || saving}
            title={dirty ? 'Re-reading the cluster replaces your edits' : undefined}
          >
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            Reload
          </Button>
        </div>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {applied && !error ? (
        <Notice tone="ok">
          Applied. The cluster returned the object as it now stands, including whatever it filled in
          itself.
        </Notice>
      ) : null}
      {manifest && !manifest.editable && manifest.reason ? (
        <Notice tone="info">{manifest.reason}</Notice>
      ) : null}

      {loading && !manifest ? (
        <p className="text-[13px] text-muted">Reading the object…</p>
      ) : (
        <YamlView
          value={draft}
          onChange={editing ? setDraft : undefined}
          className="min-h-[320px] flex-1"
        />
      )}

      <p className="text-[12px] text-muted">
        {editing
          ? 'Applying replaces the object through the agent tunnel under your own identity. The cluster’s RBAC decides whether you may, and the change is recorded in the audit trail.'
          : 'Read live through the agent tunnel under your own identity. Server-side bookkeeping — managed fields and kubectl’s last-applied copy — is stripped.'}
      </p>
    </Sheet>
  )
}
