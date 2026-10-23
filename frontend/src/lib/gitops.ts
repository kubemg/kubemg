import type { GitopsHealth, GitopsRecord, GitopsResource, GitopsSync, GitopsTool } from '../api/gitops'
import type { ResourceCategory, ResourceKey } from './resources'
import type { Tone } from './status'

/**
 * Words and tones for GitOps sync state, and the one place a controller's
 * entry is turned into an address the console can open.
 *
 * Amber is drift, rust is a broken workload, sage is settled; `unknown` is
 * idle, never sage — for Flux it is the honest answer, not a pass.
 */

export const TOOL_LABEL: Record<GitopsTool, string> = {
  argocd: 'Argo CD',
  'flux-kustomization': 'Flux Kustomization',
  'flux-helmrelease': 'Flux HelmRelease',
}

export const SYNC_LABEL: Record<GitopsSync, string> = {
  synced: 'Synced',
  out_of_sync: 'Out of sync',
  unknown: 'Unknown',
}

export const HEALTH_LABEL: Record<GitopsHealth, string> = {
  healthy: 'Healthy',
  progressing: 'Progressing',
  degraded: 'Degraded',
  suspended: 'Suspended',
  missing: 'Missing',
  unknown: 'Unknown',
}

export function syncTone(sync: GitopsSync): Tone {
  switch (sync) {
    case 'synced':
      return 'ok'
    case 'out_of_sync':
      return 'warn'
    default:
      return 'idle'
  }
}

export function healthTone(health: GitopsHealth): Tone {
  switch (health) {
    case 'healthy':
      return 'ok'
    case 'degraded':
    case 'missing':
      return 'bad'
    default:
      return 'idle'
  }
}

/** Whether a record is Flux's, whose per-object state is never recorded. */
export function isFlux(tool: GitopsTool): boolean {
  return tool !== 'argocd'
}

/** One line naming where a record's desired state comes from. */
export function sourceLine(record: GitopsRecord): string {
  const first = record.sources[0]
  if (!first) return ''
  const where = [first.repo, first.chart ?? first.path].filter(Boolean).join(' · ')
  const line = first.target_revision ? `${where} @ ${first.target_revision}` : where
  return record.sources.length > 1 ? `${line} (+${record.sources.length - 1} more)` : line
}

/** What happens to a hand edit, in the record's own terms. */
export function autoHealSentence(record: GitopsRecord): string {
  switch (record.tool) {
    case 'argocd':
      if (record.auto_heal === null) return 'Argo CD did not say whether self-heal is on.'
      return record.auto_heal
        ? 'Self-heal is on: Argo CD puts a hand edit back.'
        : 'Self-heal is off: a hand edit stays until the next sync.'
    case 'flux-kustomization':
      return record.suspended
        ? 'Suspended: Flux will not put a hand edit back until it is resumed.'
        : 'Flux puts a hand edit back at its next reconcile.'
    case 'flux-helmrelease':
      return record.auto_heal
        ? 'Drift detection is on: helm-controller puts a hand edit back.'
        : 'Drift detection is not correcting: a hand edit stays until the next upgrade.'
  }
}

/** Why an entry's own state reads `unknown`, where the tool explains it. */
export function perObjectNote(tool: GitopsTool): string | null {
  switch (tool) {
    case 'flux-kustomization':
      return 'Flux corrects drift rather than recording it, so the state of each object is unknown here.'
    case 'flux-helmrelease':
      return 'helm-controller does not report the objects a release made.'
    default:
      return null
  }
}

/** The entries that differ from what Git declares. */
export function driftedResources(record: GitopsRecord): GitopsResource[] {
  return record.resources.filter((resource) => resource.sync === 'out_of_sync')
}

/** A short phrase for what of the record a scoped grant cannot see. */
export function hiddenNote(record: GitopsRecord): string | null {
  if (record.resources_hidden === 0) return null
  const n = record.resources_hidden
  return `${n} ${n === 1 ? 'object is' : 'objects are'} outside your grant and not shown.`
}

/** The built-in kinds the console has a list for, keyed `group/Kind`. */
const BUILTIN_KEYS: Record<string, ResourceKey> = {
  '/Pod': 'pods',
  'apps/Deployment': 'deployments',
  'apps/StatefulSet': 'statefulsets',
  'apps/DaemonSet': 'daemonsets',
  'apps/ReplicaSet': 'replicasets',
  'batch/Job': 'jobs',
  'batch/CronJob': 'cronjobs',
  '/Service': 'services',
  '/ConfigMap': 'configmaps',
  '/Secret': 'secrets',
  '/PersistentVolumeClaim': 'persistentvolumeclaims',
  '/PersistentVolume': 'persistentvolumes',
  '/ServiceAccount': 'serviceaccounts',
  '/ResourceQuota': 'resourcequotas',
  '/LimitRange': 'limitranges',
  '/Node': 'nodes',
  'networking.k8s.io/Ingress': 'ingresses',
  'networking.k8s.io/NetworkPolicy': 'networkpolicies',
  'storage.k8s.io/StorageClass': 'storageclasses',
  'autoscaling/HorizontalPodAutoscaler': 'horizontalpodautoscalers',
  'policy/PodDisruptionBudget': 'poddisruptionbudgets',
  'rbac.authorization.k8s.io/Role': 'roles',
  'rbac.authorization.k8s.io/RoleBinding': 'rolebindings',
  'rbac.authorization.k8s.io/ClusterRole': 'clusterroles',
  'rbac.authorization.k8s.io/ClusterRoleBinding': 'clusterrolebindings',
  'apiextensions.k8s.io/CustomResourceDefinition': 'crds',
  'gateway.networking.k8s.io/HTTPRoute': 'httproutes',
  'networking.istio.io/VirtualService': 'virtualservices',
}

/**
 * The console's key for a controller's entry, or null for a kind it has no
 * list for. A CRD-served kind is found in the cluster's own discovered
 * inventory by group and Kind — never guessed into a plural.
 */
export function resourceKeyFor(
  entry: Pick<GitopsResource, 'group' | 'kind'>,
  discovered: ResourceCategory[] = [],
): ResourceKey | null {
  const builtin = BUILTIN_KEYS[`${entry.group}/${entry.kind}`]
  if (builtin) return builtin
  if (!entry.group) return null
  for (const category of discovered) {
    const item = category.items.find(
      (candidate) => candidate.custom?.group === entry.group && candidate.singular === entry.kind,
    )
    if (item) return item.key
  }
  return null
}
