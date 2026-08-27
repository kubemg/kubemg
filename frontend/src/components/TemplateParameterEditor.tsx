import { Plus, Trash2 } from 'lucide-react'
import type { TemplateParameter } from '../api/types'
import { Button, Select, TextInput } from './primitives'

/**
 * Editing a template's declared parameter set — one row per parameter, plain
 * fields rather than a schema builder, because a template's parameters are a
 * handful of things a manifest holds a placeholder for, not a form worth its
 * own designer. Shared between `SaveAsTemplateSheet`, where the set arrives as
 * a suggestion, and the admin editor in `AppTemplates`, where it starts from
 * nothing or from what a previous save left.
 */
export function TemplateParameterEditor({
  parameters,
  onChange,
}: {
  parameters: TemplateParameter[]
  onChange: (next: TemplateParameter[]) => void
}) {
  function update(index: number, patch: Partial<TemplateParameter>) {
    onChange(parameters.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)))
  }

  function remove(index: number) {
    onChange(parameters.filter((_, i) => i !== index))
  }

  function add() {
    onChange([...parameters, { name: '', type: 'string', required: false }])
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="label">Parameters</span>
        <Button type="button" size="sm" onClick={add}>
          <Plus aria-hidden="true" className="size-3.5" />
          Add parameter
        </Button>
      </div>
      {parameters.length === 0 ? (
        <p className="text-[12.5px] text-muted">
          No parameters. A template with none renders exactly as stored below.
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {parameters.map((parameter, index) => (
            <li
              key={index}
              className="flex flex-wrap items-center gap-2 rounded-control border border-line-soft px-3 py-2"
            >
              <TextInput
                aria-label="Parameter name"
                className="w-40 font-mono text-[12.5px]"
                value={parameter.name}
                placeholder="name"
                onChange={(event) => update(index, { name: event.target.value })}
              />
              <TextInput
                aria-label="Label"
                className="w-40"
                value={parameter.label ?? ''}
                placeholder="Label shown to the caller"
                onChange={(event) => update(index, { label: event.target.value })}
              />
              <Select
                aria-label="Type"
                size="sm"
                className="w-28"
                value={parameter.type}
                onChange={(event) =>
                  update(index, { type: event.target.value as TemplateParameter['type'] })
                }
              >
                <option value="string">string</option>
                <option value="number">number</option>
              </Select>
              <TextInput
                aria-label="Default"
                className="w-32"
                value={parameter.default ?? ''}
                placeholder="default"
                onChange={(event) => update(index, { default: event.target.value })}
              />
              <label className="flex items-center gap-1.5 text-[12.5px] text-fg">
                <input
                  type="checkbox"
                  className="size-4 accent-[var(--color-accent)]"
                  checked={parameter.required}
                  onChange={(event) => update(index, { required: event.target.checked })}
                />
                required
              </label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="ml-auto"
                onClick={() => remove(index)}
              >
                <Trash2 aria-hidden="true" className="size-3.5" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
