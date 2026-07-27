/**
 * The Explore inventory: what an operator can browse in a cluster, grouped the
 * way the work groups rather than the way the Kubernetes API does. This is the
 * single source of truth — the sidebar renders it, and the page reads scope from
 * it to decide whether a namespace even applies.
 */

export type ResourceKey =
  | 'pods'
  | 'deployments'
  | 'statefulsets'
  | 'daemonsets'
  | 'jobs'
  | 'cronjobs'
  | 'services'
  | 'ingresses'
  | 'httproutes'
  | 'virtualservices'
  | 'persistentvolumes'
  | 'persistentvolumeclaims'
  | 'storageclasses'
  | 'configmaps'
  | 'secrets'
  | 'crds'
  | 'nodes'
  | 'namespaces'

export type CategoryId = 'workloads' | 'networking' | 'storage' | 'custom' | 'cluster'

export interface ResourceItem {
  key: ResourceKey
  label: string
  /** Cluster-scoped resources have no namespace, so the namespace picker hides. */
  scope: 'namespaced' | 'cluster'
  /** Extra words the sidebar filter should match, for the Kubernetes short names. */
  aliases?: string[]
  /** Set for resources served by a CRD that a cluster may not have installed. */
  optional?: boolean
}

export interface ResourceCategory {
  id: CategoryId
  label: string
  items: ResourceItem[]
}

export const RESOURCE_CATEGORIES: ResourceCategory[] = [
  {
    id: 'workloads',
    label: 'Workloads',
    items: [
      { key: 'pods', label: 'Pods', scope: 'namespaced', aliases: ['po'] },
      { key: 'deployments', label: 'Deployments', scope: 'namespaced', aliases: ['deploy'] },
      { key: 'statefulsets', label: 'StatefulSets', scope: 'namespaced', aliases: ['sts'] },
      { key: 'daemonsets', label: 'DaemonSets', scope: 'namespaced', aliases: ['ds'] },
      { key: 'jobs', label: 'Jobs', scope: 'namespaced' },
      { key: 'cronjobs', label: 'CronJobs', scope: 'namespaced', aliases: ['cj'] },
    ],
  },
  {
    id: 'networking',
    label: 'Networking',
    items: [
      { key: 'services', label: 'Services', scope: 'namespaced', aliases: ['svc'] },
      { key: 'ingresses', label: 'Ingresses', scope: 'namespaced', aliases: ['ing'] },
      {
        key: 'httproutes',
        label: 'HTTPRoutes',
        scope: 'namespaced',
        aliases: ['gateway api'],
        optional: true,
      },
      {
        key: 'virtualservices',
        label: 'VirtualServices',
        scope: 'namespaced',
        aliases: ['istio', 'vs'],
        optional: true,
      },
    ],
  },
  {
    id: 'storage',
    label: 'Storage & Config',
    items: [
      { key: 'persistentvolumes', label: 'PersistentVolumes', scope: 'cluster', aliases: ['pv'] },
      {
        key: 'persistentvolumeclaims',
        label: 'PersistentVolumeClaims',
        scope: 'namespaced',
        aliases: ['pvc'],
      },
      { key: 'storageclasses', label: 'StorageClasses', scope: 'cluster', aliases: ['sc'] },
      { key: 'configmaps', label: 'ConfigMaps', scope: 'namespaced', aliases: ['cm'] },
      { key: 'secrets', label: 'Secrets', scope: 'namespaced' },
    ],
  },
  {
    id: 'custom',
    label: 'Custom Resources',
    items: [
      {
        key: 'crds',
        label: 'CRDs',
        scope: 'cluster',
        aliases: ['customresourcedefinitions', 'crd'],
      },
    ],
  },
  {
    id: 'cluster',
    label: 'Cluster',
    items: [
      { key: 'nodes', label: 'Nodes', scope: 'cluster', aliases: ['no'] },
      { key: 'namespaces', label: 'Namespaces', scope: 'cluster', aliases: ['ns'] },
    ],
  },
]

const BY_KEY = new Map<ResourceKey, ResourceItem>(
  RESOURCE_CATEGORIES.flatMap((category) => category.items.map((item) => [item.key, item])),
)

export function resourceItem(key: ResourceKey): ResourceItem {
  const item = BY_KEY.get(key)
  if (!item) throw new Error(`unknown resource ${key}`)
  return item
}

/** matchesResource powers the sidebar filter: label or Kubernetes short name. */
export function matchesResource(item: ResourceItem, needle: string): boolean {
  if (!needle) return true
  const query = needle.toLowerCase()
  return (
    item.label.toLowerCase().includes(query) ||
    item.key.includes(query) ||
    (item.aliases ?? []).some((alias) => alias.includes(query))
  )
}
