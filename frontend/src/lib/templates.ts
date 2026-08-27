import type { AppTemplate, RenderedObject, TemplateParameter } from '../api/types'
import type { ResourceCategory, ResourceItem, ResourceKey } from './resources'
import { resourceSingular } from './resources'

/**
 * Everything about a template's parameters that does not need a component to
 * compute it — filling in defaults, deciding whether a value is fit to render
 * with, and finding the address a rendered object is created at. Kept out of
 * `TemplateSheet` so it is the kind of logic a test can hold once the frontend
 * has somewhere to put one, per the house rule.
 */

export type TemplateValues = Record<string, string>

/** What a fresh set of controls opens on: every parameter's own default, or
    empty for one that has none. */
export function defaultTemplateValues(parameters: TemplateParameter[]): TemplateValues {
  const values: TemplateValues = {}
  for (const parameter of parameters) {
    values[parameter.name] = parameter.default ?? ''
  }
  return values
}

/** A required parameter with nothing in it — the button's gate, and a render
    request the server would refuse anyway. */
export function missingParameters(
  parameters: TemplateParameter[],
  values: TemplateValues,
): TemplateParameter[] {
  return parameters.filter((parameter) => parameter.required && (values[parameter.name] ?? '').trim() === '')
}

/** A `number` parameter holding something that is not one. Only checked when
    there is something to check — an optional, empty field is not a bad number,
    it is an absent one. */
export function invalidNumberParameters(
  parameters: TemplateParameter[],
  values: TemplateValues,
): TemplateParameter[] {
  return parameters.filter((parameter) => {
    if (parameter.type !== 'number') return false
    const value = values[parameter.name] ?? ''
    if (value.trim() === '') return false
    return Number.isNaN(Number(value))
  })
}

/** Whether Render may be pressed at all. */
export function templateValuesReady(parameters: TemplateParameter[], values: TemplateValues): boolean {
  return missingParameters(parameters, values).length === 0 &&
    invalidNumberParameters(parameters, values).length === 0
}

/** What a template picker line names it by — the title when the author gave
    one, the slug otherwise. */
export function templateDisplayName(template: AppTemplate): string {
  return template.title?.trim() || template.name
}

/**
 * Slugging a title into the name a template is stored and addressed by:
 * lowercase, digits, dashes and dots, the same shape `HelmInstallSheet` already
 * asks a release name to hold — a template and a release are both a name in a
 * flat namespace with no directory structure to disambiguate them.
 */
export const TEMPLATE_NAME = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/

export function slugifyTemplateName(input: string): string {
  return input
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9.]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
}

/**
 * Resolving a rendered object's own `kind` back to the address the existing
 * per-object create call needs: a `ResourceKey`, exactly what the sidebar
 * addresses the same kind by. It is looked up against *this cluster's*
 * inventory — the fixed table plus whatever its own CRDs discovered — rather
 * than guessed from the API group, because a CRD's registered plural is not
 * reliably derivable from its Kind and getting it wrong would address nothing.
 *
 * `undefined` means this console has no address for the kind on this cluster:
 * not a kind KubeMG refuses, just one neither the fixed inventory nor this
 * cluster's own discovery can place — an operator not installed here, most
 * often. The caller reports that rather than guessing an address that would
 * 404.
 */
export function resourceItemForKind(
  categories: ResourceCategory[],
  kind: string,
): ResourceItem | undefined {
  for (const category of categories) {
    for (const item of category.items) {
      if (resourceSingular(item) === kind) return item
    }
  }
  return undefined
}

export function resourceKeyForObject(
  categories: ResourceCategory[],
  object: RenderedObject,
): ResourceKey | undefined {
  return resourceItemForKind(categories, object.kind)?.key
}
