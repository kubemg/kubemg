/**
 * The Explore inventory: what an operator can browse in a cluster, grouped the
 * way the work groups rather than the way the Kubernetes API does. This is the
 * single source of truth — the sidebar renders it, and the page reads scope from
 * it to decide whether a namespace even applies.
 */

/**
 * A resource served by a CustomResourceDefinition, keyed by the API it is
 * actually served under. These cannot be part of the fixed union: which CRDs a
 * cluster has is a property of the cluster, read from it at runtime, so the key
 * carries the API rather than naming an entry in a table compiled into the app.
 */
export type CustomResourceKey = `crd:${string}`

export type ResourceKey =
  | CustomResourceKey
  | 'helmreleases'
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

export type CategoryId =
  | 'workloads'
  | 'helm'
  | 'networking'
  | 'storage'
  | 'custom'
  | 'cluster'
  | 'istio'
  | 'other'

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
  /**
   * Set where KubeMG will show a manifest but not write it back. Only Secrets:
   * their values are redacted before they leave the cluster, so the manifest is
   * not the whole object and applying it would overwrite every value with the
   * placeholder standing in for it. The backend refuses the write too — this is
   * only what keeps the button from being offered.
   */
  manifestReadOnly?: boolean
  /**
   * Set on an item discovered from a cluster's CRD list rather than declared
   * above. It carries the API the resource is served under, which is what the
   * generic read and the manifest editor address it by.
   */
  custom?: CustomResourceRef
}

/** The API one CustomResourceDefinition serves, as a list read addresses it. */
export interface CustomResourceRef {
  group: string
  version: string
  plural: string
  scope: 'namespaced' | 'cluster'
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
    /*
     * Helm is its own section rather than a row under Workloads because a
     * release is not a workload — it is the thing that produced several of
     * them, and an operator looking for "what is installed here" is asking a
     * different question from "what is running here".
     *
     * There is no CRD behind it and no controller: Helm 3 keeps a release as a
     * labelled Secret, so this is browsable on any cluster whose grant may read
     * Secrets. It has no manifest of its own to edit — a release's editable
     * surface is its values, which the table opens directly.
     */
    id: 'helm',
    label: 'Helm',
    items: [
      {
        key: 'helmreleases',
        label: 'Releases',
        singular: 'Release',
        scope: 'namespaced',
        aliases: ['helm', 'charts', 'helmreleases'],
      },
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

/**
 * The CRD-served kinds KubeMG has a *real* reader for, keyed by the API they are
 * served under. They are deliberately not in RESOURCE_CATEGORIES: a cluster
 * either has the CRD or it does not, and an entry that is always shown and
 * usually answers "not installed" is a worse sidebar than one that only lists
 * what is there. Discovery places them, and because they carry a fixed key
 * rather than a `crd:` one they keep their normalised table — hostnames,
 * gateways and rules — instead of falling back to the generic name/kind/age one.
 */
const RICH_CRD_ITEMS: Record<string, ResourceItem> = {
  'httproutes.gateway.networking.k8s.io': {
    key: 'httproutes',
    label: 'HTTPRoutes',
    scope: 'namespaced',
    aliases: ['gateway api'],
  },
  'virtualservices.networking.istio.io': {
    key: 'virtualservices',
    label: 'VirtualServices',
    scope: 'namespaced',
    aliases: ['istio', 'vs'],
  },
}

const BY_KEY = new Map<ResourceKey, ResourceItem>([
  ...RESOURCE_CATEGORIES.flatMap((category) =>
    category.items.map((item): [ResourceKey, ResourceItem] => [item.key, item]),
  ),
  ...Object.values(RICH_CRD_ITEMS).map((item): [ResourceKey, ResourceItem] => [item.key, item]),
])

/**
 * resourceItem resolves a key to what the page needs to know about it: its
 * label, and whether a namespace applies. `discovered` are the per-cluster
 * categories built from that cluster's CRDs, which is where a `crd:` key lives —
 * so a key from one cluster resolves to nothing on a cluster without that CRD,
 * which is exactly the answer the page acts on.
 */
export function resourceItem(
  key: ResourceKey,
  discovered: ResourceCategory[] = [],
): ResourceItem | null {
  const fixed = BY_KEY.get(key)
  if (fixed) return fixed
  for (const category of discovered) {
    const item = category.items.find((entry) => entry.key === key)
    if (item) return item
  }
  return null
}

/* ------------------------------------------------- discovered from a cluster --- */

/** ISTIO_GROUP_SUFFIX matches every Istio API group: networking, security, telemetry. */
const ISTIO_GROUP_SUFFIX = '.istio.io'

/** The Gateway API is a CRD too, and belongs with the networking it does. */
const GATEWAY_API_GROUP = 'gateway.networking.k8s.io'

/** customResourceKey names a CRD-served resource by the API it is served under. */
export function customResourceKey(ref: CustomResourceRef): CustomResourceKey {
  return `crd:${ref.group}/${ref.version}/${ref.plural}`
}

/** A CustomResourceDefinition as the cluster reports it. */
interface DiscoveredCRD {
  group: string
  kind: string
  plural: string
  scope: string
  versions: string[]
}

/**
 * pluralLabel renders a CRD's Kind as the plural a list is titled with —
 * `VirtualService` → `VirtualServices`, `ServiceEntry` → `ServiceEntries`. The
 * CRD's own `plural` is all lowercase, which reads wrong next to the fixed
 * entries; the Kind carries the casing an operator recognises.
 */
function pluralLabel(kind: string): string {
  if (/[^aeiou]y$/i.test(kind)) return `${kind.slice(0, -1)}ies`
  if (/(s|x|z|ch|sh)$/i.test(kind)) return `${kind}es`
  return `${kind}s`
}

/** customResourceItem turns one CRD into a browsable sidebar entry. */
function customResourceItem(crd: DiscoveredCRD): ResourceItem | null {
  // A CRD with no served version cannot be read at all; its versions list is
  // already filtered to the served ones by the backend.
  const version = preferredVersion(crd.versions)
  if (!version) return null

  const ref: CustomResourceRef = {
    group: crd.group,
    version,
    plural: crd.plural,
    scope: crd.scope === 'Cluster' ? 'cluster' : 'namespaced',
  }
  return {
    key: customResourceKey(ref),
    label: pluralLabel(crd.kind),
    singular: crd.kind,
    scope: ref.scope,
    // The group is what tells two same-named CRDs apart, so it has to be
    // findable even though the label cannot carry it.
    aliases: [crd.plural, crd.group, `${crd.plural}.${crd.group}`],
    custom: ref,
  }
}

/**
 * preferredVersion picks which version of a CRD to read: the newest stable one,
 * falling back to the newest pre-release. `v2` beats `v1`, and `v1` beats
 * `v1beta1` — the same order kubectl's discovery prefers.
 */
function preferredVersion(versions: string[]): string | null {
  const ranked = [...versions].sort((a, b) => versionRank(b) - versionRank(a))
  return ranked[0] ?? null
}

function versionRank(version: string): number {
  const parsed = /^v(\d+)(?:(alpha|beta)(\d+))?$/.exec(version)
  if (!parsed) return 0
  const major = Number(parsed[1])
  if (!parsed[2]) return major * 1000
  return major * 1000 - (parsed[2] === 'beta' ? 100 : 200) + Number(parsed[3])
}

/**
 * discoverCategories builds the sidebar sections that only exist because of what
 * a particular cluster has installed. Three destinations, by API group:
 *
 *   - Istio (`*.istio.io`) gets a section of its own, because on a mesh cluster
 *     those are a whole area of the operator's work rather than a footnote.
 *   - The Gateway API joins Networking, where the thing it does already lives.
 *   - Everything else lands in **Other**, at the bottom. A cluster can define a
 *     hundred kinds and most of them belong to one operator's internals; they
 *     are worth reaching, not worth putting above Workloads.
 *
 * Nothing here shadows a fixed entry, because the kinds that would have — the
 * Gateway API's and Istio's — are no longer declared as fixed entries at all.
 * They live in RICH_CRD_ITEMS and are placed by this function, so they appear
 * exactly when the cluster serves them and still get their proper table.
 */
export function discoverCategories(crds: DiscoveredCRD[]): ResourceCategory[] {
  const sections: Record<string, ResourceItem[]> = { istio: [], networking: [], other: [] }

  for (const crd of crds) {
    const rich = RICH_CRD_ITEMS[`${crd.plural}.${crd.group}`]
    const item = rich ?? customResourceItem(crd)
    if (item) sections[categoryFor(crd.group)].push(item)
  }

  const byLabel = (a: ResourceItem, b: ResourceItem) => a.label.localeCompare(b.label)
  const out: ResourceCategory[] = []
  if (sections.networking.length > 0) {
    out.push({ id: 'networking', label: 'Networking', items: sections.networking.sort(byLabel) })
  }
  if (sections.istio.length > 0) {
    out.push({ id: 'istio', label: 'Istio', items: sections.istio.sort(byLabel) })
  }
  if (sections.other.length > 0) {
    out.push({ id: 'other', label: 'Other', items: sections.other.sort(byLabel) })
  }
  return out
}

/** categoryFor decides which section a CRD's API group belongs in. */
function categoryFor(group: string): 'istio' | 'networking' | 'other' {
  if (group.endsWith(ISTIO_GROUP_SUFFIX)) return 'istio'
  if (group === GATEWAY_API_GROUP) return 'networking'
  return 'other'
}

/**
 * exploreCategories merges the fixed inventory with what a cluster turned out to
 * have. A discovered section carrying a fixed section's id extends it, so the
 * Gateway API's routes sit under Networking with Services and Ingresses. The two
 * sections with no fixed counterpart are placed by what they mean: Istio next to
 * the networking it extends, and Other last, since it is a residue rather than a
 * named area.
 */
export function exploreCategories(discovered: ResourceCategory[]): ResourceCategory[] {
  const extra = new Map(discovered.map((category) => [category.id, category]))

  const merged = RESOURCE_CATEGORIES.map((category) => {
    const addition = extra.get(category.id)
    if (!addition) return category
    extra.delete(category.id)
    return { ...category, items: [...category.items, ...addition.items] }
  })

  const istio = extra.get('istio')
  if (istio) {
    const at = merged.findIndex((category) => category.id === 'networking')
    merged.splice(at < 0 ? merged.length : at + 1, 0, istio)
  }

  const other = extra.get('other')
  if (other) merged.push(other)
  return merged
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
