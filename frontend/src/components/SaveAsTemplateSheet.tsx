import { useEffect, useState } from 'react'
import { Plus } from 'lucide-react'
import { draftAppTemplate, errorMessage, putAppTemplate } from '../api/client'
import type { AppTemplateInput, TemplateParameter } from '../api/types'
import { TEMPLATE_NAME, slugifyTemplateName } from '../lib/templates'
import { Button, Field, Notice, Sheet, Spinner, TextArea, TextInput } from './primitives'
import { TemplateParameterEditor } from './TemplateParameterEditor'
import { YamlView } from './YamlView'

/**
 * Saving a template from an object already in the cluster — the path the
 * roadmap calls the point, rather than a template starting life on a blank
 * page. The object's own manifest is what the server drafts from: it strips
 * the bookkeeping that belongs to this cluster's record of the object, not to
 * a bundle meant to be created anywhere, and suggests parameters for what
 * looks like a name, an image or a replica count. Both come back editable,
 * because the suggestion is a starting point, never a verdict.
 */
export function SaveAsTemplateSheet({
  yaml,
  kind,
  objectName,
  onClose,
  onSaved,
}: {
  /** The object's own manifest, exactly as the YAML tab holds it. */
  yaml: string
  kind: string
  objectName: string
  onClose: () => void
  onSaved: () => void
}) {
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const [manifests, setManifests] = useState('')
  const [parameters, setParameters] = useState<TemplateParameter[]>([])

  const [name, setName] = useState('')
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    draftAppTemplate(yaml)
      .then((result) => {
        if (cancelled) return
        setManifests(result.manifests)
        setParameters(result.parameters)
        setName(slugifyTemplateName(`${kind}-${objectName}`))
        setTitle(`${kind}: ${objectName}`)
      })
      .catch((err) => {
        if (!cancelled) setLoadError(errorMessage(err, 'Could not draft a template from this object.'))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // Drafted once, from the manifest this sheet opened on — re-running it on
    // every keystroke below would replace edits the operator just made.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [yaml, kind, objectName])

  const nameValid = TEMPLATE_NAME.test(name)

  async function save() {
    setSaving(true)
    setSaveError(null)
    try {
      const input: AppTemplateInput = {
        name: name.trim(),
        title: title.trim() || undefined,
        description: description.trim() || undefined,
        manifests,
        parameters,
      }
      await putAppTemplate(name.trim(), input)
      onSaved()
    } catch (err) {
      setSaveError(errorMessage(err, 'Could not save this template.'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet
      width="xl"
      eyebrow="Application catalogue"
      title="Save as template"
      onClose={onClose}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="primary"
            onClick={() => void save()}
            disabled={saving || loading || !nameValid || manifests.trim() === ''}
          >
            {saving ? <Spinner className="size-3.5" /> : <Plus aria-hidden="true" className="size-3.5" />}
            Save template
          </Button>
        </>
      }
    >
      {loadError ? <Notice tone="error">{loadError}</Notice> : null}
      {saveError ? <Notice tone="error">{saveError}</Notice> : null}

      {loading ? (
        <p className="text-[13px] text-muted">Drafting a template from this object…</p>
      ) : (
        <>
          <Notice tone="info">
            The object's own identity — name, namespace, status and every field the API server owns
            rather than the author — is stripped: it belongs to this cluster's record of the object,
            not to a bundle meant to be created anywhere.
          </Notice>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Name"
              htmlFor="tpl_save_name"
              error={name && !nameValid ? 'Lowercase letters, digits, dashes and dots.' : undefined}
              hint="The address this template is stored and rendered from."
            >
              <TextInput
                id="tpl_save_name"
                className="font-mono text-[12.5px]"
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field label="Title" htmlFor="tpl_save_title" hint="What the picker shows instead of the name.">
              <TextInput id="tpl_save_title" value={title} onChange={(event) => setTitle(event.target.value)} />
            </Field>
          </div>

          <Field label="Description" htmlFor="tpl_save_description">
            <TextArea
              id="tpl_save_description"
              rows={2}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Field>

          <TemplateParameterEditor parameters={parameters} onChange={setParameters} />

          <div className="flex flex-col gap-1.5">
            <span className="label">Manifests</span>
            <YamlView value={manifests} onChange={setManifests} className="min-h-[320px] flex-1" />
          </div>
        </>
      )}
    </Sheet>
  )
}
