import { useCallback, useEffect, useState } from 'react'
import { Check, Copy, LayoutTemplate, Pencil, RefreshCw, ShieldAlert, Undo2, X } from 'lucide-react'
import type { GuardrailRefusal } from '../api/client'
import {
  errorMessage,
  fetchResourceYaml,
  guardrailRefusal,
  previewResourceObjectDiff,
  updateResourceYaml,
} from '../api/client'
import type { Cluster, ManifestDiff, ResourceManifest } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { useAuth } from '../state/auth-context'
import { ManifestDiffView } from './ManifestDiffView'
import { Button, Notice, Pill, Spinner } from './primitives'
import { SaveAsTemplateSheet } from './SaveAsTemplateSheet'
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
  const { user } = useAuth()
  const isAdmin = user?.role === 'admin'

  const [manifest, setManifest] = useState<ResourceManifest | null>(null)
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(startEditing)
  // Admin-only, the same class of act as writing to the template catalogue
  // itself: a template is offered to the whole fleet, so turning one object
  // into one is not something a `view` grant gets to decide.
  const [savingTemplate, setSavingTemplate] = useState(false)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applied, setApplied] = useState(false)
  const [copied, setCopied] = useState(false)

  // The confirmation step: Apply no longer writes directly, it opens a review
  // of what would change. `reviewing` gates the editor back to read-only —
  // typing over a diff mid-review would make it describe a manifest that is
  // no longer the one on screen.
  const [reviewing, setReviewing] = useState(false)
  const [diff, setDiff] = useState<ManifestDiff | null>(null)
  const [diffLoading, setDiffLoading] = useState(false)
  const [diffError, setDiffError] = useState<string | null>(null)
  // Set only when the write was refused by a KubeMG guardrail rather than by
  // the cluster's own RBAC — see guardrailRefusal. It stays beside the diff
  // that is still on screen, since a policy is far easier to understand next
  // to the change that triggered it than in a banner with nothing beside it.
  const [guardrail, setGuardrail] = useState<GuardrailRefusal | null>(null)

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

  // openReview asks what this manifest would actually change, against the
  // cluster as it stands right now — not against `manifest`, which may be
  // stale by the time Apply is clicked. The diff is fetched fresh rather than
  // computed here in TypeScript so the change an operator approves is
  // guaranteed to be the one objdiff.Diff would also compute for the stored
  // audit row: one implementation, asked twice.
  async function openReview() {
    setReviewing(true)
    setDiff(null)
    setDiffError(null)
    setGuardrail(null)
    setDiffLoading(true)
    try {
      const result = await previewResourceObjectDiff(cluster.id, kind, name, namespace, draft)
      setDiff(result)
    } catch (err) {
      // A diff that failed to compute — a stale resourceVersion, a namespace
      // this grant cannot read — is not a reason to block the write outright:
      // the write goes through the cluster's own RBAC either way. It is shown
      // so the operator knows the confirmation below is not backed by one.
      setDiffError(errorMessage(err, 'Could not compute a diff against the cluster.'))
    } finally {
      setDiffLoading(false)
    }
  }

  function cancelReview() {
    setReviewing(false)
    setDiff(null)
    setDiffError(null)
    setGuardrail(null)
  }

  async function save() {
    if (!manifest) return

    setSaving(true)
    setApplied(false)
    setGuardrail(null)
    try {
      const result = await updateResourceYaml(cluster.id, kind, name, namespace, draft)
      setManifest(result)
      setDraft(result.yaml)
      setEditing(false)
      setApplied(true)
      setError(null)
      setReviewing(false)
      setDiff(null)
      await onApplied?.()
    } catch (err) {
      // A guardrail refusal is KubeMG's own policy, not the cluster's RBAC —
      // surfaced beside the diff that is still on screen rather than folded
      // into the same sentence as a bare cluster refusal, since the two call
      // for different next steps (ask an admin about the rule vs. ask for a
      // wider grant).
      const refusal = guardrailRefusal(err)
      if (refusal) {
        setGuardrail(refusal)
      } else {
        // The cluster's own refusal is the useful one — "deployments.apps is
        // forbidden" says far more than anything invented here would.
        setError(errorMessage(err, 'The cluster did not accept this manifest.'))
      }
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
          {/* Absent, not disabled, for anybody who is not an admin — the same
              rule every other admin-only action in this console follows: a
              button that always refuses is worse than no button. */}
          {isAdmin && !editing ? (
            <Button
              type="button"
              size="sm"
              onClick={() => setSavingTemplate(true)}
              disabled={!manifest}
            >
              <LayoutTemplate aria-hidden="true" className="size-3.5" />
              Save as template
            </Button>
          ) : null}
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
            disabled={loading || saving || reviewing}
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
                disabled={!dirty || saving || reviewing}
              >
                <Undo2 aria-hidden="true" className="size-3.5" />
                Revert
              </Button>
              {reviewing ? (
                <>
                  <Button type="button" size="sm" onClick={cancelReview} disabled={saving}>
                    <X aria-hidden="true" className="size-3.5" />
                    Cancel
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="primary"
                    onClick={() => void save()}
                    disabled={saving || diffLoading}
                  >
                    {saving ? (
                      <Spinner className="size-3.5" />
                    ) : (
                      <Check aria-hidden="true" className="size-3.5" />
                    )}
                    Confirm and apply
                  </Button>
                </>
              ) : (
                <Button
                  type="button"
                  size="sm"
                  variant="primary"
                  onClick={() => void openReview()}
                  disabled={!dirty || saving}
                >
                  <Check aria-hidden="true" className="size-3.5" />
                  Apply to cluster
                </Button>
              )}
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

      {reviewing ? (
        <div className="flex flex-col gap-2 rounded-control border border-line-soft bg-raised p-3">
          <p className="text-[13px] font-medium text-fg">Review before applying</p>
          {diffLoading ? (
            <p className="text-[13px] text-muted">Comparing against the cluster…</p>
          ) : diffError ? (
            <Notice tone="warn">{diffError}</Notice>
          ) : diff ? (
            <ManifestDiffView diff={diff} emptyLabel="This manifest matches the object on the cluster — nothing would change." />
          ) : null}
          {guardrail ? (
            <Notice tone="error">
              <span className="inline-flex items-start gap-1.5">
                <ShieldAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
                <span>
                  Blocked by the guardrail policy <strong>{guardrail.policy || 'unnamed'}</strong>
                  {guardrail.scope ? ` (${guardrail.scope})` : ''}. {guardrail.message}
                </span>
              </span>
            </Notice>
          ) : null}
        </div>
      ) : null}

      {loading && !manifest ? (
        <p className="text-[13px] text-muted">Reading the object…</p>
      ) : (
        <YamlView
          // Reviewing freezes the draft: a keystroke against a diff that is
          // already on screen would make it describe a manifest that no
          // longer matches what is displayed.
          value={draft}
          onChange={editing && !reviewing ? setDraft : undefined}
          className="min-h-[320px] flex-1"
        />
      )}

      <p className="text-[12px] text-muted">
        {reviewing
          ? 'Confirming replaces the object through the agent tunnel under your own identity. The cluster’s RBAC decides whether you may, and the change is recorded in the audit trail.'
          : editing
            ? 'Apply reviews the change before writing it — nothing reaches the cluster until you confirm.'
            : 'Read live through the agent tunnel under your own identity. Server-side bookkeeping — managed fields and kubectl’s last-applied copy — is stripped.'}
      </p>

      {savingTemplate && manifest ? (
        <SaveAsTemplateSheet
          yaml={draft}
          kind={manifest.kind}
          objectName={manifest.name}
          onClose={() => setSavingTemplate(false)}
          onSaved={() => setSavingTemplate(false)}
        />
      ) : null}
    </div>
  )
}
