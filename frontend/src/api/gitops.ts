import type { ManagedBy, ManifestDiff } from './types'

/**
 * GitOps sync state, as the controllers recorded it and the server narrowed it.
 *
 * Nothing here is KubeMG's own comparison: Argo CD wrote `sync` per resource,
 * Flux wrote which revision it applied. A Flux entry's `sync` is `unknown`
 * because Flux corrects drift rather than recording it — the page says so, and
 * nothing here turns it into `synced`.
 */

export type GitopsTool = 'argocd' | 'flux-kustomization' | 'flux-helmrelease'

export type GitopsSync = 'synced' | 'out_of_sync' | 'unknown'

export type GitopsHealth = 'healthy' | 'progressing' | 'degraded' | 'suspended' | 'missing' | 'unknown'

export interface GitopsSource {
  /** Repository URL (credentials removed), or for Flux the source object it names. */
  repo?: string
  path?: string
  chart?: string
  target_revision?: string
}

export interface GitopsResource {
  group: string
  kind: string
  namespace?: string
  name: string
  sync: GitopsSync
  health?: GitopsHealth
}

export interface GitopsRecord {
  tool: GitopsTool
  namespace: string
  name: string
  project?: string
  sources: GitopsSource[]
  revision?: string
  /** Flux's last attempt, present only when it is not what was applied. */
  attempted_revision?: string
  sync: GitopsSync
  health: GitopsHealth
  /** Whether a hand edit is put back. Null when the record does not say. */
  auto_heal: boolean | null
  suspended: boolean
  reconciled_at?: string
  message?: string
  /** The destination cluster, when the record deploys somewhere other than here. */
  remote?: string
  resources: GitopsResource[]
  /** Entries outside the caller's grant, counted rather than shown. */
  resources_hidden: number
  resources_truncated: boolean
}

export interface GitopsToolStatus {
  tool: GitopsTool
  installed: boolean
  available: boolean
  reason?: string
}

export interface GitopsSummary {
  records: number
  out_of_sync: number
  degraded: number
}

export interface GitopsState {
  available: boolean
  tools: GitopsToolStatus[]
  records: GitopsRecord[]
  summary: GitopsSummary
  truncated?: boolean
}

export interface GitopsObject {
  tracked: boolean
  managed_by: ManagedBy | null
  record: GitopsRecord | null
  entry: GitopsResource | null
  reason?: string
  /** Whether live and desired state can be compared. Set only for an object an
      Argo CD record tracks; absent otherwise. */
  diff?: GitopsDiffOffer
}

export interface GitopsDiffOffer {
  available: boolean
  reason?: string
}

/**
 * One object's live state against the state Argo CD would sync it to. `diff`
 * reads live → desired, in the shape the manifest editor's diff has; `live`
 * and `desired` are both sides as YAML with what the cluster writes stripped.
 */
export interface GitopsDiff {
  available: boolean
  reason?: string
  application?: { namespace: string; name: string }
  /** Argo CD's own verdict on this object. */
  modified: boolean
  diff?: ManifestDiff
  live: string
  desired: string
  live_truncated: boolean
  desired_truncated: boolean
}

export function completeGitopsDiff(data: Partial<GitopsDiff>): GitopsDiff {
  return {
    available: data.available ?? false,
    reason: data.reason,
    application: data.application,
    modified: data.modified ?? false,
    diff: data.diff ? { changes: data.diff.changes ?? [], truncated: data.diff.truncated ?? false } : undefined,
    live: data.live ?? '',
    desired: data.desired ?? '',
    live_truncated: data.live_truncated ?? false,
    desired_truncated: data.desired_truncated ?? false,
  }
}

export function completeGitopsRecord(record: Partial<GitopsRecord>): GitopsRecord {
  return {
    tool: record.tool ?? 'argocd',
    namespace: record.namespace ?? '',
    name: record.name ?? '',
    project: record.project,
    sources: record.sources ?? [],
    revision: record.revision,
    attempted_revision: record.attempted_revision,
    sync: record.sync ?? 'unknown',
    health: record.health ?? 'unknown',
    auto_heal: record.auto_heal ?? null,
    suspended: record.suspended ?? false,
    reconciled_at: record.reconciled_at,
    message: record.message,
    remote: record.remote,
    resources: record.resources ?? [],
    resources_hidden: record.resources_hidden ?? 0,
    resources_truncated: record.resources_truncated ?? false,
  }
}

export function completeGitopsState(data: Partial<GitopsState>): GitopsState {
  return {
    available: data.available ?? false,
    tools: data.tools ?? [],
    records: (data.records ?? []).map(completeGitopsRecord),
    summary: data.summary ?? { records: 0, out_of_sync: 0, degraded: 0 },
    truncated: data.truncated,
  }
}

export function completeGitopsObject(data: Partial<GitopsObject>): GitopsObject {
  return {
    tracked: data.tracked ?? false,
    managed_by: data.managed_by ?? null,
    record: data.record ? completeGitopsRecord(data.record) : null,
    entry: data.entry ?? null,
    reason: data.reason,
    diff: data.diff,
  }
}
