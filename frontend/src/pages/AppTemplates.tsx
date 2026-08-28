import { useCallback, useEffect, useState } from 'react'
import { LayoutTemplate, Pencil, Plus, Trash2 } from 'lucide-react'
import {
  deleteAppTemplate,
  errorMessage,
  listAppTemplates,
  putAppTemplate,
} from '../api/client'
import type { AppTemplate, AppTemplateInput, TemplateParameter } from '../api/types'
import { AppShell } from '../components/AppShell'
import { TemplateParameterEditor } from '../components/TemplateParameterEditor'
import { YamlView } from '../components/YamlView'
import { TEMPLATE_NAME, templateDisplayName } from '../lib/templates'
import { relativeAge } from '../lib/time'
import {
  Button,
  EmptyState,
  Field,
  IconButton,
  Notice,
  Pill,
  Row,
  Sheet,
  Spinner,
  Table,
  Td,
  TextArea,
  TextInput,
  Th,
} from '../components/primitives'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'

/**
 * AppTemplates is where the application catalogue itself lives: every stored
 * template, admin-written, listed to everyone from `TemplateSheet` out in
 * Explore. It is the administration half of Phase 6.5 — the other half,
 * `SaveAsTemplateSheet`, is what actually populates it in practice, since a
 * template built from an object already running is the path this feature
 * exists for. This page still has to exist on its own, though: something has
 * to hold the first template before any object exists to save one from, and
 * something has to let a seeded one be edited or removed like any other row.
 */
export function AppTemplates() {
  const confirm = useConfirm()
  const report = useResult()
  const [templates, setTemplates] = useState<AppTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<AppTemplate | 'new' | null>(null)
  const [busyName, setBusyName] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setTemplates(await listAppTemplates())
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the template catalogue.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function remove(template: AppTemplate) {
    const confirmed = await confirm({
      eyebrow: 'Application template',
      title: `Delete “${templateDisplayName(template)}”?`,
      body: 'Anybody opening “From template” in Explore stops seeing it immediately. Objects already created from it are unaffected. A seeded template stays deleted.',
      confirmLabel: 'Delete',
    })
    if (!confirmed) return
    setBusyName(template.name)
    try {
      await deleteAppTemplate(template.name)
      await load()
      report({
        tone: 'ok',
        title: `Deleted ${templateDisplayName(template)}`,
        body: 'It is gone from “From template” in Explore. Objects already created from it are unaffected.',
      })
    } catch (err) {
      const message = errorMessage(err, `Could not delete ${templateDisplayName(template)}.`)
      setError(message)
      report({ tone: 'error', title: `${templateDisplayName(template)} was not deleted`, body: message })
    } finally {
      setBusyName(null)
    }
  }

  return (
    <AppShell
      title="App templates"
      actions={
        <Button variant="primary" onClick={() => setEditing('new')}>
          <Plus aria-hidden="true" className="size-4" />
          New template
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Notice tone="info">
          A template renders to YAML and stops there — filling in its parameters produces manifests
          that land in an editor, and creating them is the same per-object create call every manifest
          editor here already makes. Listed to everyone signed in; only an admin writes one, here or
          by saving one from an object already in a cluster.
        </Notice>

        <div className="card min-w-0 overflow-hidden">
          <Table>
            <thead>
              <tr>
                <Th className="w-[28%]">Name</Th>
                <Th className="hidden md:table-cell">Description</Th>
                <Th className="w-[12%]">Parameters</Th>
                <Th className="w-[14%]">Updated</Th>
                <Th className="w-[16%] text-right">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {templates.map((template) => {
                const busy = busyName === template.name
                return (
                  <Row key={template.name}>
                    <Td className="truncate">
                      <button
                        type="button"
                        className="flex flex-col items-start gap-0.5 text-left"
                        onClick={() => setEditing(template)}
                      >
                        <span className="flex items-center gap-1.5 truncate text-[13.5px] font-medium text-fg hover:text-accent">
                          {templateDisplayName(template)}
                        </span>
                        <span className="flex items-center gap-1.5">
                          <span className="font-mono text-[11.5px] text-faint">{template.name}</span>
                          {template.seeded ? <Pill tone="idle">seeded</Pill> : null}
                        </span>
                      </button>
                    </Td>
                    <Td className="hidden truncate text-[12.5px] text-muted md:table-cell">
                      {template.description ?? '—'}
                    </Td>
                    <Td className="font-mono text-[12.5px] text-muted">{template.parameters.length}</Td>
                    <Td className="text-[12.5px] text-muted">{relativeAge(template.updated_at)}</Td>
                    <Td className="text-right">
                      <span className="flex justify-end gap-1">
                        <IconButton
                          label={`Edit ${templateDisplayName(template)}`}
                          onClick={() => setEditing(template)}
                          disabled={busy}
                        >
                          <Pencil aria-hidden="true" className="size-4" />
                        </IconButton>
                        <IconButton
                          label={`Delete ${templateDisplayName(template)}`}
                          tone="danger"
                          onClick={() => void remove(template)}
                          disabled={busy}
                        >
                          <Trash2 aria-hidden="true" className="size-4" />
                        </IconButton>
                      </span>
                    </Td>
                  </Row>
                )
              })}
            </tbody>
          </Table>

          {!loading && templates.length === 0 ? (
            <EmptyState icon={<LayoutTemplate aria-hidden="true" className="size-5" />} title="No templates yet">
              Add one from scratch, or open an object already in a cluster and save its manifest as a
              template — that is the path most templates here come from.
            </EmptyState>
          ) : null}
        </div>
      </div>

      {editing ? (
        <TemplateEditorSheet
          template={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
          }}
        />
      ) : null}
    </AppShell>
  )
}

function TemplateEditorSheet({
  template,
  onClose,
  onSaved,
}: {
  template: AppTemplate | null
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [name, setName] = useState(template?.name ?? '')
  const [title, setTitle] = useState(template?.title ?? '')
  const [description, setDescription] = useState(template?.description ?? '')
  const [manifests, setManifests] = useState(template?.manifests ?? '')
  const [parameters, setParameters] = useState<TemplateParameter[]>(template?.parameters ?? [])

  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const nameValid = TEMPLATE_NAME.test(name)

  async function save() {
    setSaving(true)
    setError(null)
    try {
      const input: AppTemplateInput = {
        name: name.trim(),
        title: title.trim() || undefined,
        description: description.trim() || undefined,
        manifests,
        parameters,
      }
      await putAppTemplate(name.trim(), input)
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save this template.'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet
      width="xl"
      eyebrow="App templates"
      title={template ? templateDisplayName(template) : 'New template'}
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
            disabled={saving || !nameValid || manifests.trim() === ''}
          >
            {saving ? <Spinner className="size-3.5" /> : <Plus aria-hidden="true" className="size-3.5" />}
            {template ? 'Save changes' : 'Create template'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Name"
          htmlFor="tpl_admin_name"
          error={name && !nameValid ? 'Lowercase letters, digits, dashes and dots.' : undefined}
          hint={
            template
              ? 'The address this template is stored and rendered from — renaming means writing a new one.'
              : 'What this template is addressed by when it is rendered.'
          }
        >
          <TextInput
            id="tpl_admin_name"
            className="font-mono text-[12.5px]"
            value={name}
            disabled={template !== null}
            onChange={(event) => setName(event.target.value)}
            placeholder="nginx-deployment"
          />
        </Field>
        <Field label="Title" htmlFor="tpl_admin_title" hint="What the picker shows instead of the name.">
          <TextInput id="tpl_admin_title" value={title} onChange={(event) => setTitle(event.target.value)} />
        </Field>
      </div>

      <Field label="Description" htmlFor="tpl_admin_description">
        <TextArea
          id="tpl_admin_description"
          rows={2}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>

      <TemplateParameterEditor parameters={parameters} onChange={setParameters} />

      <div className="flex flex-col gap-1.5">
        <span className="label">Manifests</span>
        <YamlView value={manifests} onChange={setManifests} className="min-h-[320px] flex-1" />
        <p className="text-[12px] text-muted">
          One or more documents, separated by <span className="font-mono">---</span>. A placeholder
          matches a declared parameter's name; the render call refuses one that is undeclared or, if
          required, left unfilled.
        </p>
      </div>
    </Sheet>
  )
}
