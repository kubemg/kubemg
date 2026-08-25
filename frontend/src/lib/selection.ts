import type { ResourceKey } from './resources'
import { workloadCapability } from './workloads'

/*
 * Acting on more than one row at a time.
 *
 * Everything else in Explore is addressed one object at a time, and that is the
 * right shape for reading: a list is browsed, and the thing worth opening is
 * opened. Changing is different. The pods left behind by a rollout, the six
 * failed Jobs from last night, the schedules that have to stop before a
 * migration — each of those is one decision an operator has already made about
 * a set, and answering it one row at a time is the same decision typed out N
 * times, with the Nth one done carelessly because the first six were fine.
 *
 * Two rules keep it from being the dangerous half of the console. The
 * checkboxes are **off until they are asked for** — a list is read far more
 * often than it is acted on, and a column of checkboxes on every list makes a
 * destructive act one stray click away from a read. And an action is offered
 * only where **every** selected row answers for it: a mixed selection narrows
 * what can be done to it rather than acting on the part that fits, because a
 * button that silently skips half a selection is worse than one that is absent.
 */

/**
 * One selected row. It carries what an action needs to address the object —
 * which is more than a name, since the workload list serves three Kinds at once
 * and a CronJob's row is the only place its current schedule state is known.
 */
export interface SelectedRow {
  /** Unique within a selection: kind, namespace and name together. */
  key: string
  kind: ResourceKey
  /** The singular Kind, for the words: "Delete 3 Pods". */
  label: string
  name: string
  namespace?: string
  /** Whether a schedule is currently off. Set on CronJob rows and nowhere else. */
  suspended?: boolean
}

/** selectionKey is how a row is identified inside a selection. */
export function selectionKey(kind: string, namespace: string | undefined, name: string): string {
  return `${kind}/${namespace ?? ''}/${name}`
}

/**
 * What can be asked of a selection. Delete is the one every kind answers for —
 * it is the same call the manifest editor's address makes — and the rest are
 * the workload controls, each offered only where the whole selection has it.
 */
export type BulkActionName = 'delete' | 'restart' | 'suspend' | 'resume'

/**
 * bulkActions says what a selection can be asked to do. Suspend and resume are
 * the two halves of one switch and both can be offered at once: a mixed
 * selection of running and suspended CronJobs is exactly the case where an
 * operator wants "all of these off", and each row that is already there is
 * answered by the server without a write.
 */
export function bulkActions(rows: SelectedRow[]): BulkActionName[] {
  if (rows.length === 0) return []

  const capabilities = rows.map((row) => workloadCapability(row.kind))
  const out: BulkActionName[] = []

  if (capabilities.every((capability) => capability?.restart)) out.push('restart')
  if (capabilities.every((capability) => capability?.suspend)) {
    if (rows.some((row) => !row.suspended)) out.push('suspend')
    if (rows.some((row) => row.suspended)) out.push('resume')
  }
  // Last, and visually apart from the others wherever it is drawn: it is the
  // one action here that cannot be undone by pressing the other button.
  out.push('delete')
  return out
}

/** The word on the button, and the sentence the confirmation opens with. */
export const BULK_ACTION_LABEL: Record<BulkActionName, string> = {
  delete: 'Delete',
  restart: 'Restart',
  suspend: 'Suspend',
  resume: 'Resume',
}
