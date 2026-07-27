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

/**
 * The namespace selection that means "everything I may see". A namespace cannot
 * be named `*`, so it can never collide with a real one. The backend turns it
 * into one cluster-wide read for an unscoped grant, or one read per granted
 * namespace for a scoped one — never a cluster-wide read past a scope.
 */
export const ALL_NAMESPACES = '*'

export interface ResourceItem {
  key: ResourceKey
  label: string
  /** Only where dropping the trailing "s" would not give the singular. */
  singular?: string
  /** Cluster-scoped resources have no namespace, so the namespace picker hides. */
  scope: 'namespaced' | 'cluster'
  /** Extra words the sidebar filter should match, for the Kubernetes short names. */
  aliases?: string[]
  /** Set for resources served by a CRD that a cluster may not have installed. */
  optional?: boolean
  /**
   * Set where KubeMG will show a manifest but not write it back. Only Secrets:
   * their values are redacted before they leave the cluster, so the manifest is
   * not the whole object and applying it would overwrite every value with the
   * placeholder standing in for it. The backend refuses the write too — this is
   * only what keeps the button from being offered.
   */
  manifestReadOnly?: boolean
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
      {
        key: 'ingresses',
        label: 'Ingresses',
        singular: 'Ingress',
        scope: 'namespaced',
        aliases: ['ing'],
      },
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
      {
        key: 'storageclasses',
        label: 'StorageClasses',
        singular: 'StorageClass',
        scope: 'cluster',
        aliases: ['sc'],
      },
      { key: 'configmaps', label: 'ConfigMaps', scope: 'namespaced', aliases: ['cm'] },
      { key: 'secrets', label: 'Secrets', scope: 'namespaced', manifestReadOnly: true },
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

/** resourceSingular names one object of a kind, for a drawer over one row. */
export function resourceSingular(item: ResourceItem): string {
  return item.singular ?? item.label.replace(/s$/, '')
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
