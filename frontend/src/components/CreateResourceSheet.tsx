import { useId, useState } from 'react'
import { Check, Plus, RotateCcw, ShieldAlert } from 'lucide-react'
import type { GuardrailRefusal } from '../api/client'
import { createResourceObject, errorMessage, guardrailRefusal } from '../api/client'
import type { Cluster, ResourceManifest } from '../api/types'
import { manifestTemplate } from '../lib/manifests'
import type { ResourceItem } from '../lib/resources'
import { resourceSingular } from '../lib/resources'
import { Button, Field, Notice, Select, Sheet, Spinner } from './primitives'
import { YamlView } from './YamlView'

/**
 * Creating one object, from the manifest for it.
 *
 * This is the manifest editor opened on a document nothing has read — the same
 * write path, addressed at a collection rather than at an object — because
 * `kubectl create -f` was the one thing the tunnel already carried that the
 * console could not do. It is a sheet rather than a tab in the detail drawer for
 * the reason the drawer exists at all: that drawer is *about an object*, and
 * there is no object here yet.
 *
 * Two things it deliberately is not. It is **not a form**: a per-kind form is a
 * schema KubeMG would have to hold for every kind including ones nobody here has
 * heard of, and it would quietly cap what can be created at whatever fields
 * somebody thought of. And there is **no diff step**, unlike an edit: a diff
 * answers "what does this change", and against an object that does not exist yet
 * the answer is the manifest already on screen.
 *
 * Nothing here is a new permission. The POST goes down the same impersonated
 * tunnel, so a `view` grant is refused by the cluster's own RBAC in the
 * cluster's own words, it passes the same guardrails, and it lands in the audit
 * trail as a `create`.
 */
export function CreateResourceSheet({
  cluster,
  item,
  namespace,
  namespaces,
  onClose,
  onCreated,
}: {
  cluster: Cluster
  item: ResourceItem
  /** The namespace the list is open on, or undefined for a cluster-scoped kind. */
  namespace?: string
  /** What the namespace picker may offer — the caller's granted namespaces. */
  namespaces: string[]
  onClose: () => void
  /** Called once the cluster has stored the object, so the list behind re-reads. */
  onCreated: (created: ResourceManifest) => Promise<void> | void
}) {
  const namespaced = item.scope === 'namespaced'
  const singular = resourceSingular(item)
  const fieldId = useId()

  const [draft, setDraft] = useState(() => manifestTemplate(item))
  // The namespace is part of the *address*, not of the manifest — the same rule
  // every read here follows — so it is a control above the editor rather than a
  // line inside it that could disagree with it.
  const [target, setTarget] = useState(
    () => namespace ?? (namespaces.length > 0 ? namespaces[0] : ''),
  )

  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [guardrail, setGuardrail] = useState<GuardrailRefusal | null>(null)
  const [created, setCreated] = useState<ResourceManifest | null>(null)

  const missingNamespace = namespaced && target.trim() === ''

  async function submit() {
    if (missingNamespace) return
    setSaving(true)
    setError(null)
    setGuardrail(null)
    try {
      const result = await createResourceObject(
        cluster.id,
        item.key,
        namespaced ? target : undefined,
        draft,
      )
      setCreated(result)
      await onCreated(result)
    } catch (err) {
      // KubeMG's own policy and the cluster's RBAC are two different refusals
      // calling for two different next steps — ask an admin about the rule, or
      // ask for a wider grant — so they are never folded into one sentence.
      const refusal = guardrailRefusal(err)
      if (refusal) setGuardrail(refusal)
      else setError(errorMessage(err, 'The cluster did not accept this manifest.'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet
      width="xl"
      eyebrow={cluster.name}
      title={`Create ${singular}`}
      onClose={onClose}
      footer={
        created ? (
          <Button type="button" variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button type="button" variant="ghost" onClick={() => setDraft(manifestTemplate(item))}>
              <RotateCcw aria-hidden="true" className="size-3.5" />
              Reset to template
            </Button>
            <Button type="button" onClick={onClose} disabled={saving}>
              Cancel
            </Button>
            <Button
              type="button"
              variant="primary"
              onClick={() => void submit()}
              disabled={saving || draft.trim() === '' || missingNamespace}
            >
              {saving ? (
                <Spinner className="size-3.5" />
              ) : (
                <Plus aria-hidden="true" className="size-3.5" />
              )}
              Create
            </Button>
          </>
        )
      }
    >
      {namespaced ? (
        <Field
          label="Namespace"
          htmlFor={fieldId}
          hint={
            missingNamespace
              ? undefined
              : 'Where this object is created. Leave metadata.namespace out of the manifest — this is the namespace it is checked against.'
          }
          error={missingNamespace ? 'No namespace is available for this cluster and grant.' : undefined}
        >
          <Select
            id={fieldId}
            value={target}
            disabled={saving || created !== null || namespaces.length === 0}
            onChange={(event) => setTarget(event.target.value)}
          >
            {namespaces.length === 0 ? <option value="">No namespaces</option> : null}
            {namespaces.map((entry) => (
              <option key={entry} value={entry}>
                {entry}
              </option>
            ))}
          </Select>
        </Field>
      ) : null}

      {created ? (
        <Notice tone="ok">
          <span className="inline-flex items-start gap-1.5">
            <Check aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
            <span>
              Created <span className="font-mono">{created.name}</span>
              {created.namespace ? (
                <>
                  {' '}
                  in <span className="font-mono">{created.namespace}</span>
                </>
              ) : null}
              . The manifest below is what the cluster stored, including everything it filled in
              itself.
            </span>
          </span>
        </Notice>
      ) : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
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

      <YamlView
        // Once the cluster has it, the editor stops being a draft: what is on
        // screen is the object as stored, and typing over it would suggest a
        // second create rather than an edit of the thing that now exists.
        value={created ? created.yaml : draft}
        onChange={created || saving ? undefined : setDraft}
        className="min-h-[420px] flex-1"
      />

      <p className="text-[12px] text-muted">
        {created
          ? 'Open it from the list to change it — the manifest editor writes to the object that now exists.'
          : 'Sent to the cluster through the agent tunnel under your own identity. The cluster’s RBAC decides whether you may create this, and the call is recorded in the audit trail as a create.'}
      </p>
    </Sheet>
  )
}
