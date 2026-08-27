import { useEffect, useMemo, useState } from 'react'
import { ArrowLeft, Check, LayoutTemplate, Plus, ShieldAlert } from 'lucide-react'
import type { GuardrailRefusal } from '../api/client'
import {
  createResourceObject,
  errorMessage,
  guardrailRefusal,
  listAppTemplates,
  renderAppTemplate,
} from '../api/client'
import type { AppTemplate, RenderedObject } from '../api/types'
import type { Cluster } from '../api/types'
import {
  defaultTemplateValues,
  invalidNumberParameters,
  missingParameters,
  resourceItemForKind,
  templateDisplayName,
  templateValuesReady,
} from '../lib/templates'
import type { TemplateValues } from '../lib/templates'
import type { ResourceCategory } from '../lib/resources'
import { Button, Field, Notice, Pill, Row, Select, Sheet, Spinner, Table, Td, TextInput, Th } from './primitives'
import { YamlView } from './YamlView'

/** One object's own path through the run, once Create has been pressed. */
type ObjectStatus =
  | { state: 'pending' }
  | { state: 'creating' }
  | { state: 'created' }
  | { state: 'refused'; message: string }
  | { state: 'skipped' }

/**
 * Filling in a template and creating what it renders — the use half of the
 * application catalogue. A template renders to YAML and then stops: from there
 * this is the same write `CreateResourceSheet` already makes, one object at a
 * time, so a `view` grant is refused by the cluster's own RBAC in the cluster's
 * own words and every object lands in the audit trail as its own `create`.
 *
 * Three steps live in one sheet because they are one decision read in stages —
 * which template, then what it needs, then what it produced — rather than three
 * things to navigate between. Nothing here is a diff step or a form for the
 * kind: the rendered manifests are the same free-text editors every other
 * write in this console opens, so a template is a starting point, never a cage.
 */
export function TemplateSheet({
  cluster,
  namespace,
  namespaces,
  categories,
  onClose,
  onCreated,
}: {
  cluster: Cluster
  /** The namespace the list this was opened from is scoped to, if any. */
  namespace?: string
  namespaces: string[]
  /** This cluster's inventory — fixed plus discovered — which is what resolves
      a rendered object's kind back to an address `createResourceObject` can use. */
  categories: ResourceCategory[]
  onClose: () => void
  /** Called once at least one object has landed, so the list behind re-reads. */
  onCreated: () => Promise<void> | void
}) {
  const [templates, setTemplates] = useState<AppTemplate[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)

  const [selected, setSelected] = useState<AppTemplate | null>(null)
  const [values, setValues] = useState<TemplateValues>({})

  const [rendering, setRendering] = useState(false)
  const [renderError, setRenderError] = useState<string | null>(null)
  const [objects, setObjects] = useState<RenderedObject[] | null>(null)
  // Independent from `objects`: the rendered YAML is still what gets created,
  // but every editor stays editable after the render the same way Create's own
  // manifest editor is.
  const [drafts, setDrafts] = useState<string[]>([])

  const [target, setTarget] = useState(namespace ?? (namespaces[0] ?? ''))

  const [statuses, setStatuses] = useState<ObjectStatus[]>([])
  const [running, setRunning] = useState(false)
  const [guardrail, setGuardrail] = useState<GuardrailRefusal | null>(null)
  const [anyCreated, setAnyCreated] = useState(false)

  useEffect(() => {
    void listAppTemplates()
      .then((result) => setTemplates(result))
      .catch((err) => setListError(errorMessage(err, 'Could not read the template catalogue.')))
  }, [])

  function pick(template: AppTemplate) {
    setSelected(template)
    setValues(defaultTemplateValues(template.parameters))
    setRenderError(null)
    setObjects(null)
    setDrafts([])
    setStatuses([])
    setGuardrail(null)
  }

  function backToParams() {
    setObjects(null)
    setDrafts([])
    setStatuses([])
    setGuardrail(null)
  }

  async function render() {
    if (!selected) return
    setRendering(true)
    setRenderError(null)
    try {
      const result = await renderAppTemplate(selected.name, values)
      setObjects(result.objects)
      setDrafts(result.objects.map((object) => object.yaml))
      setStatuses(result.objects.map(() => ({ state: 'pending' })))
      // Most bundles carry at least one namespaced object; the picker above
      // them is offered whenever that could be true, exactly as it is for a
      // hand-typed manifest.
      if (!target && namespaces.length > 0) setTarget(namespaces[0])
    } catch (err) {
      setRenderError(errorMessage(err, 'The template could not be rendered.'))
    } finally {
      setRendering(false)
    }
  }

  const namespaceNeeded = useMemo(() => {
    if (!objects) return false
    return objects.some((object) => resourceItemForKind(categories, object.kind)?.scope !== 'cluster')
  }, [objects, categories])

  const missing = selected ? missingParameters(selected.parameters, values) : []
  const invalidNumbers = selected ? invalidNumberParameters(selected.parameters, values) : []
  const ready = selected ? templateValuesReady(selected.parameters, values) : false
  const missingNamespace = namespaceNeeded && target.trim() === ''

  /**
   * Creating every object, one at a time, in the order the template declared
   * them. A failure stops the run — the same rule `pkg/api/resources_helm.go`
   * follows for a chart's own objects — and everything after it reports as
   * skipped rather than silently attempted.
   */
  async function createAll() {
    if (!objects) return
    setRunning(true)
    setGuardrail(null)
    let created = false
    let stopped = false
    const next: ObjectStatus[] = objects.map(() => ({ state: 'pending' }))
    setStatuses([...next])

    for (let index = 0; index < objects.length; index += 1) {
      if (stopped) {
        next[index] = { state: 'skipped' }
        setStatuses([...next])
        continue
      }
      const object = objects[index]
      const item = resourceItemForKind(categories, object.kind)
      if (!item) {
        next[index] = {
          state: 'refused',
          message: `No address for kind ${object.kind} in this cluster's inventory.`,
        }
        setStatuses([...next])
        stopped = true
        continue
      }
      const namespaced = item.scope === 'namespaced'
      if (namespaced && !target.trim()) {
        next[index] = { state: 'refused', message: 'No namespace is available for this cluster and grant.' }
        setStatuses([...next])
        stopped = true
        continue
      }

      next[index] = { state: 'creating' }
      setStatuses([...next])
      try {
        await createResourceObject(cluster.id, item.key, namespaced ? target : undefined, drafts[index])
        next[index] = { state: 'created' }
        created = true
        setStatuses([...next])
      } catch (err) {
        const refusal = guardrailRefusal(err)
        if (refusal) setGuardrail(refusal)
        next[index] = {
          state: 'refused',
          message: refusal ? refusal.message : errorMessage(err, 'The cluster did not accept this manifest.'),
        }
        setStatuses([...next])
        stopped = true
      }
    }

    setRunning(false)
    setAnyCreated(created)
    if (created) await onCreated()
  }

  const finished = statuses.length > 0 && !running && statuses.every((entry) => entry.state !== 'pending' && entry.state !== 'creating')

  return (
    <Sheet
      width="xl"
      eyebrow={cluster.name}
      title={selected ? templateDisplayName(selected) : 'Create from a template'}
      onClose={onClose}
      footer={
        !selected ? (
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
        ) : !objects ? (
          <>
            <Button type="button" onClick={() => setSelected(null)} disabled={rendering}>
              <ArrowLeft aria-hidden="true" className="size-3.5" />
              Back
            </Button>
            <Button
              type="button"
              variant="primary"
              onClick={() => void render()}
              disabled={rendering || !ready}
            >
              {rendering ? <Spinner className="size-3.5" /> : <Check aria-hidden="true" className="size-3.5" />}
              Render
            </Button>
          </>
        ) : finished ? (
          <Button type="button" variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button type="button" onClick={backToParams} disabled={running}>
              <ArrowLeft aria-hidden="true" className="size-3.5" />
              Back
            </Button>
            <Button type="button" onClick={onClose} disabled={running}>
              Cancel
            </Button>
            <Button
              type="button"
              variant="primary"
              onClick={() => void createAll()}
              disabled={running || missingNamespace}
            >
              {running ? <Spinner className="size-3.5" /> : <Plus aria-hidden="true" className="size-3.5" />}
              Create objects
            </Button>
          </>
        )
      }
    >
      {!selected ? (
        <TemplatePicker templates={templates} error={listError} onPick={pick} />
      ) : !objects ? (
        <div className="flex flex-col gap-4">
          {selected.description ? <p className="text-[13px] text-muted">{selected.description}</p> : null}
          {renderError ? <Notice tone="error">{renderError}</Notice> : null}
          {selected.parameters.length === 0 ? (
            <Notice tone="info">This template declares no parameters — it renders exactly as stored.</Notice>
          ) : (
            selected.parameters.map((parameter) => {
              const invalid = invalidNumbers.some((entry) => entry.name === parameter.name)
              const isMissing = missing.some((entry) => entry.name === parameter.name)
              return (
                <Field
                  key={parameter.name}
                  label={parameter.label || parameter.name}
                  htmlFor={`tpl_param_${parameter.name}`}
                  hint={parameter.description}
                  error={
                    invalid
                      ? 'Enter a number.'
                      : isMissing
                        ? 'Required.'
                        : undefined
                  }
                >
                  <TextInput
                    id={`tpl_param_${parameter.name}`}
                    type={parameter.type === 'number' ? 'number' : 'text'}
                    value={values[parameter.name] ?? ''}
                    onChange={(event) =>
                      setValues((current) => ({ ...current, [parameter.name]: event.target.value }))
                    }
                    placeholder={parameter.default}
                  />
                </Field>
              )
            })
          )}
        </div>
      ) : (
        <div className="flex flex-col gap-4">
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

          {namespaceNeeded ? (
            <Field
              label="Namespace"
              htmlFor="tpl_namespace"
              hint="Where every namespaced object below is created — the namespace is the address, not a line in any of the manifests."
              error={missingNamespace ? 'No namespace is available for this cluster and grant.' : undefined}
            >
              <Select
                id="tpl_namespace"
                value={target}
                disabled={running || finished || namespaces.length === 0}
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

          {statuses.length > 0 ? (
            <Table>
              <thead>
                <tr>
                  <Th>Kind</Th>
                  <Th>Name</Th>
                  <Th>Status</Th>
                </tr>
              </thead>
              <tbody>
                {objects.map((object, index) => (
                  <Row key={`${object.kind}/${object.name}/${index}`}>
                    <Td className="font-mono">{object.kind}</Td>
                    <Td className="truncate font-mono">{object.name}</Td>
                    <Td>
                      <ObjectStatusPill status={statuses[index]} />
                      {statuses[index]?.state === 'refused' ? (
                        <p className="mt-1 text-[12px] leading-snug text-danger">
                          {statuses[index].message}
                        </p>
                      ) : null}
                    </Td>
                  </Row>
                ))}
              </tbody>
            </Table>
          ) : null}

          {finished && anyCreated ? (
            <Notice tone="ok">
              <span className="inline-flex items-start gap-1.5">
                <Check aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
                <span>
                  Created what the cluster accepted. Open a row from the list behind this to see what
                  it stored, including everything it filled in itself.
                </span>
              </span>
            </Notice>
          ) : null}
          {finished && !anyCreated ? (
            <Notice tone="error">Nothing was created — see the refusal above.</Notice>
          ) : null}

          <div className="flex flex-col gap-4">
            {objects.map((object, index) => (
              <div key={`${object.kind}/${object.name}/${index}`} className="flex flex-col gap-1.5">
                <span className="text-[13px] font-medium text-fg">
                  {object.kind} <span className="font-mono text-muted">{object.name}</span>
                  {object.namespace ? (
                    <span className="ml-1.5 font-mono text-[12px] text-faint">{object.namespace}</span>
                  ) : null}
                </span>
                <YamlView
                  value={drafts[index]}
                  onChange={
                    running || finished
                      ? undefined
                      : (next) =>
                          setDrafts((current) => {
                            const copy = [...current]
                            copy[index] = next
                            return copy
                          })
                  }
                  className="min-h-[220px]"
                />
              </div>
            ))}
          </div>
        </div>
      )}
    </Sheet>
  )
}

function ObjectStatusPill({ status }: { status: ObjectStatus | undefined }) {
  switch (status?.state) {
    case 'created':
      return <Pill tone="ok">created</Pill>
    case 'creating':
      return <Pill tone="accent">creating…</Pill>
    case 'refused':
      return <Pill tone="bad">refused</Pill>
    case 'skipped':
      return <Pill tone="idle">skipped</Pill>
    default:
      return <Pill tone="idle">pending</Pill>
  }
}

function TemplatePicker({
  templates,
  error,
  onPick,
}: {
  templates: AppTemplate[] | null
  error: string | null
  onPick: (template: AppTemplate) => void
}) {
  if (error) return <Notice tone="error">{error}</Notice>
  if (!templates) return <p className="text-[13px] text-muted">Reading the template catalogue…</p>
  if (templates.length === 0) {
    return (
      <Notice tone="info">
        No templates yet. An admin adds one from the app templates page — or by saving one from an
        object already in a cluster.
      </Notice>
    )
  }

  return (
    <ul className="flex flex-col gap-2">
      {templates.map((template) => (
        <li key={template.name}>
          <button
            type="button"
            onClick={() => onPick(template)}
            className="flex w-full flex-col gap-1 rounded-control border border-line-soft px-3 py-2.5 text-left transition-colors hover:border-faint/60 hover:bg-raised"
          >
            <span className="flex items-center gap-2">
              <LayoutTemplate aria-hidden="true" className="size-4 shrink-0 text-muted" />
              <span className="truncate text-[13.5px] font-medium text-fg">
                {templateDisplayName(template)}
              </span>
              {template.seeded ? <Pill tone="idle">seeded</Pill> : null}
            </span>
            {template.description ? (
              <span className="truncate text-[12.5px] text-muted">{template.description}</span>
            ) : null}
          </button>
        </li>
      ))}
    </ul>
  )
}
