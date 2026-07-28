import { useCallback, useEffect, useState } from 'react'
import { Check, Copy, Pencil, RefreshCw, Undo2 } from 'lucide-react'
import { errorMessage, fetchResourceYaml, updateResourceYaml } from '../api/client'
import type { Cluster, ResourceManifest } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { Button, Notice, Pill, Spinner } from './primitives'
import { YamlView } from './YamlView'

/**
 * The whole object as YAML, read and written through the same tunnel as
 * everything else, impersonated — so what a caller may change is decided by the
 * target cluster's RBAC, in the cluster's own words, and lands in the audit
 * trail as an `update` on the object it touched.
 *
 * It is a panel rather than a drawer of its own because a manifest is one way of
 * looking at an object, not a thing beside it: it sits as a tab next to the
 * object's overview and its events, so moving between "what is this", "why is it
 * not ready" and "what exactly does it say" never means closing anything. Its
 * actions live in its own bar for the same reason — the enclosing sheet's footer
 * belongs to the object, not to whichever tab happens to be open.
 */
export function YamlPanel({
  cluster,
  kind,
  name,
  namespace,
  editing: startEditing = false,
  onDirtyChange,
  onApplied,
}: {
  cluster: Cluster
  kind: ResourceKey
  name: string
  namespace?: string
  /** Whether the panel opens ready to type. */
  editing?: boolean
  /**
   * Reports a half-typed manifest upward, so the drawer around it can ask before
   * closing. The panel cannot ask on its own — it is not what gets closed.
   */
  onDirtyChange?: (dirty: boolean) => void
  onApplied?: () => Promise<void> | void
}) {
  const [manifest, setManifest] = useState<ResourceManifest | null>(null)
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(startEditing)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applied, setApplied] = useState(false)
  const [copied, setCopied] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await fetchResourceYaml(cluster.id, kind, name, namespace)
      setManifest(result)
      setDraft(result.yaml)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read this object from the cluster.'))
      setManifest(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, kind, name, namespace])

  useEffect(() => {
    void load()
  }, [load])

  const dirty = manifest !== null && draft !== manifest.yaml

  useEffect(() => {
    onDirtyChange?.(dirty)
    // Unmounting clears the flag: an unmounted editor has nothing to discard,
    // and leaving it set would make the drawer ask about an edit that is gone.
    return () => onDirtyChange?.(false)
  }, [dirty, onDirtyChange])

  async function save() {
    if (!manifest) return

    setSaving(true)
    setApplied(false)
    try {
      const result = await updateResourceYaml(cluster.id, kind, name, namespace, draft)
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
    <div className="flex min-h-0 flex-1 flex-col gap-3">
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

        <div className="ml-auto flex flex-wrap items-center gap-2">
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

          {editing ? (
            <>
              <Button
                type="button"
                size="sm"
                onClick={() => setDraft(manifest?.yaml ?? '')}
                disabled={!dirty || saving}
              >
                <Undo2 aria-hidden="true" className="size-3.5" />
                Revert
              </Button>
              <Button
                type="button"
                size="sm"
                variant="primary"
                onClick={() => void save()}
                disabled={!dirty || saving}
              >
                {saving ? (
                  <Spinner className="size-3.5" />
                ) : (
                  <Check aria-hidden="true" className="size-3.5" />
                )}
                Apply to cluster
              </Button>
            </>
          ) : (
            <Button
              type="button"
              size="sm"
              variant="primary"
              onClick={() => setEditing(true)}
              disabled={!editable || loading}
            >
              <Pencil aria-hidden="true" className="size-3.5" />
              Edit config
            </Button>
          )}
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
    </div>
  )
}
