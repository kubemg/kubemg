import type { ManagedBy } from '../api/types'

/*
 * Saying, before a write, that something else writes this object too.
 *
 * The server reads who reconciles an object off its own tracking metadata (see
 * `managed_by` on the describe and on the list rows a selection acts on). This
 * module turns that into the sentence each write surface shows: what manages it,
 * what will happen to a change made here, and where a lasting change belongs.
 *
 * It is a notice, never a refusal — the autoscaler notice's rule: patching a
 * GitOps-managed object by hand during an incident is legitimate. What it
 * prevents is the write that succeeds, reports what it set, and is quietly
 * undone a minute later with nothing having said it would be.
 *
 * The words are per act because the consequence is. A delete under an operator
 * is recreated; an edit is overwritten; a rollout restart is the one change a
 * GitOps tool leaves alone — the annotation it writes is a field no manifest in
 * Git sets, so neither Argo CD's diff nor Flux's server-side apply touches it —
 * and only a controller that rewrites the whole pod template undoes it.
 */

/** The kinds of write a notice is worded for. Scale, suspend, an edited manifest
    and a rollback are all "a change to a field"; restart and delete are not. */
export type ManagedAct = 'change' | 'restart' | 'delete'

/** The managing object's name, qualified by its namespace when one is known. */
function qualified(managed: ManagedBy): string {
  return managed.namespace ? `${managed.namespace}/${managed.name}` : managed.name
}

/** What manages it, as a noun phrase: "the Argo CD application shop". */
export function managedByLabel(managed: ManagedBy): string {
  switch (managed.manager) {
    case 'controller':
      return `${managed.kind ?? 'controller'} ${managed.name}`
    case 'argocd':
      return `Argo CD application ${qualified(managed)}`
    case 'flux':
      return `Flux ${managed.kind ?? 'object'} ${qualified(managed)}`
    case 'helm':
      return `Helm release ${qualified(managed)}`
  }
}

function subject(managed: ManagedBy, label: string): string {
  const object = `This ${label}`
  switch (managed.manager) {
    case 'controller':
      return `${object} is controlled by ${managedByLabel(managed)}.`
    case 'argocd':
      return `${object} is deployed by the ${managedByLabel(managed)}.`
    case 'flux':
      return managed.kind === 'HelmRelease'
        ? `${object} is installed by the ${managedByLabel(managed)}.`
        : `${object} is applied by the ${managedByLabel(managed)}.`
    case 'helm':
      return `${object} belongs to the ${managedByLabel(managed)}.`
  }
}

function consequence(managed: ManagedBy, act: ManagedAct): string | null {
  const owner = managed.kind ?? 'owner'
  const helmRelease = managed.manager === 'flux' && managed.kind === 'HelmRelease'

  if (act === 'restart') {
    // Only a controller that rewrites the object from its own spec takes the
    // restart's annotation back off; see the file comment.
    return managed.manager === 'controller'
      ? `The restart goes through, but the ${owner} may put back its own pod template, which ` +
          'removes the restart’s annotation and rolls the pods a second time.'
      : null
  }

  if (act === 'delete') {
    switch (managed.manager) {
      case 'controller':
        return `Deleting it is not final: the ${owner} recreates it. To remove it for good, change or delete the ${owner}.`
      case 'argocd':
        return 'If the application self-heals, it is recreated on the next sync; if not, the ' +
          'application reports it missing. To remove it for good, remove it from Git.'
      case 'flux':
        if (helmRelease) {
          return 'It comes back on the next reconcile if drift detection is on, and with the next ' +
            'upgrade either way. To remove it for good, change the HelmRelease.'
        }
        return managed.reverts
          ? 'Flux recreates it on the next reconcile. To remove it for good, remove it from the source.'
          : 'Reconciliation is turned off on this object, so Flux will not recreate it until that ' +
              'annotation is removed.'
      case 'helm':
        return 'The release still lists it, and its next upgrade recreates it. To remove it for ' +
          'good, change or uninstall the release.'
    }
  }

  switch (managed.manager) {
    case 'controller':
      return `The ${owner} rewrites it from its own spec, so a change made here will be undone. ` +
        `A lasting change belongs on the ${owner}.`
    case 'argocd':
      return 'If the application self-heals, a change to a field Git sets is reverted on the next ' +
        'sync; if not, the application shows out of sync until Git agrees. A lasting change ' +
        'belongs in Git.'
    case 'flux':
      if (helmRelease) {
        return 'If drift detection is on, a change is reverted on the next reconcile; either way ' +
          'the next upgrade renders over it. A lasting change belongs in the HelmRelease’s values.'
      }
      return managed.reverts
        ? 'Flux puts back every field its source sets on each reconcile, so a change to one of ' +
            'them will be undone. A lasting change belongs in the source.'
        : 'Reconciliation is turned off on this object, so a change made here stays until that ' +
            'annotation is removed.'
    case 'helm':
      return 'The release’s next upgrade renders over a change made here. A lasting change ' +
        'belongs in the release’s values.'
  }
}

/**
 * managedNotice is the whole sentence for one object and one act, or null when
 * nothing manages it or the act is one its manager leaves alone. `label` is the
 * singular Kind, for "This Deployment …".
 */
export function managedNotice(
  managed: ManagedBy | undefined,
  label: string,
  act: ManagedAct,
): string | null {
  if (!managed) return null
  const rest = consequence(managed, act)
  return rest ? `${subject(managed, label)} ${rest}` : null
}

/** A certain revert is a warning; a possible one is information. */
export function managedTone(managed: ManagedBy): 'warn' | 'info' {
  return managed.reverts ? 'warn' : 'info'
}

/**
 * managedSelectionNotice is the one line a selection gets: how many of the
 * selected rows something else manages in a way the act runs into. The rows
 * themselves are marked individually, so this only says how many and why it
 * matters — a paragraph per row would bury the list it is about.
 */
export function managedSelectionNotice(
  rows: Array<{ managedBy?: ManagedBy; label: string }>,
  act: ManagedAct,
): { text: string; tone: 'warn' | 'info' } | null {
  const affected = rows.filter((row) => managedNotice(row.managedBy, row.label, act) !== null)
  if (affected.length === 0) return null

  // One row reads better as its own sentence than as "1 of 1".
  if (rows.length === 1) {
    const [row] = affected
    return {
      text: managedNotice(row.managedBy, row.label, act) as string,
      tone: managedTone(row.managedBy as ManagedBy),
    }
  }

  const count =
    affected.length === rows.length
      ? `All ${rows.length} of these`
      : `${affected.length} of the ${rows.length} selected`
  const outcome =
    act === 'delete'
      ? 'may be recreated by what manages them'
      : act === 'restart'
        ? `${affected.length === 1 ? 'is' : 'are'} controlled by an owner that may put the pod template back and roll the pods again`
        : 'may have this change undone by what manages them'
  return {
    text: `${count} ${outcome}. Each one is marked below with what manages it.`,
    tone: affected.some((row) => row.managedBy?.reverts) ? 'warn' : 'info',
  }
}
