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
  | 'serviceaccounts'
  | 'roles'
  | 'clusterroles'
  | 'rolebindings'
  | 'clusterrolebindings'

/**
 * A section KubeMG did not know about until it read a cluster's CRDs: one API
 * group family's kinds, keyed by that family. It cannot be part of the fixed
 * union for the same reason a `crd:` key cannot — which operators a cluster runs
 * is a property of the cluster, so the id has to carry the answer.
 */
export type OperatorCategoryId = `operator:${string}`

export type CategoryId =
  | OperatorCategoryId
  | 'workloads'
  | 'helm'
  | 'networking'
  | 'storage'
  | 'access'
  | 'custom'
  | 'cluster'
  | 'other'

/** Whether a section was discovered from a cluster rather than declared here. */
export function isOperatorCategory(id: CategoryId): id is OperatorCategoryId {
  return id.startsWith('operator:')
}

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
    /*
     * The cluster's own RBAC — a section of its own rather than rows under
     * Cluster, because it answers a different question from everything above it.
     * Every other list says what is *running*; these say who may change it, and
     * they are the one part of the inventory that describes the authorization
     * KubeMG delegates to rather than the one KubeMG enforces.
     *
     * The console's own Access section (users, groups, the permissions matrix)
     * governs KubeMG. This governs the cluster. They are deliberately not merged
     * and each says which it is, because a page that blurred them would be worse
     * than either alone: someone would read a KubeMG `view` grant as proof of
     * what the cluster will refuse.
     */
    id: 'access',
    label: 'Access (RBAC)',
    items: [
      {
        key: 'serviceaccounts',
        label: 'ServiceAccounts',
        singular: 'ServiceAccount',
        scope: 'namespaced',
        aliases: ['sa', 'serviceaccount', 'identity'],
      },
      { key: 'roles', label: 'Roles', scope: 'namespaced', aliases: ['rbac'] },
      {
        key: 'rolebindings',
        label: 'RoleBindings',
        singular: 'RoleBinding',
        scope: 'namespaced',
        aliases: ['rb', 'rbac', 'binding'],
      },
      {
        key: 'clusterroles',
        label: 'ClusterRoles',
        singular: 'ClusterRole',
        scope: 'cluster',
        aliases: ['cr', 'rbac'],
      },
      {
        key: 'clusterrolebindings',
        label: 'ClusterRoleBindings',
        singular: 'ClusterRoleBinding',
        scope: 'cluster',
        aliases: ['crb', 'rbac', 'binding'],
      },
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

/** The Gateway API is a CRD too, and belongs with the networking it does. */
const GATEWAY_API_GROUP = 'gateway.networking.k8s.io'

/**
 * How many kinds an API group family needs before it earns a section of its own.
 * An operator that installs one CRD is a row, not an area of work, and a column
 * of single-row sections is harder to read than one Other list — so a singleton
 * family stays in Other, where the filter still finds it.
 */
const MIN_OPERATOR_KINDS = 2

/**
 * Domain roots that belong to no single vendor: everyone's CRDs end up under
 * them, so the *root* is not an identity and the label before it is. Grouping by
 * `k8s.io` would put the Gateway API, Cluster API and half the ecosystem in one
 * section called after a registrar.
 */
const SHARED_GROUP_ROOTS = new Set([
  'k8s.io',
  'x-k8s.io',
  'kubernetes.io',
  'openshift.io',
  'coreos.com',
])

/**
 * Casing and spelling the derivation cannot know — nothing more. This is not a
 * registry of supported operators: a family with no entry here still gets its
 * own section, named from its own domain. Only add a line where the derived name
 * is *wrong*, not where it is merely plain.
 */
const FAMILY_LABELS: Record<string, string> = {
  'istio.io': 'Istio',
  'cert-manager.io': 'cert-manager',
  'argoproj.io': 'Argo',
  'cncf.io': 'CNCF',
}

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
 * a particular cluster has installed. Which operators those are is **not** known
 * here — a cluster may run Istio, or Strimzi, or Debezium, or something written
 * in-house — so the sections are derived from the CRDs themselves rather than
 * from a table of the vendors KubeMG has heard of:
 *
 *   - The Gateway API joins Networking, where the thing it does already lives.
 *     That is the one group named in this file, because it is the one whose kinds
 *     KubeMG reads first-class as networking rather than as somebody's operator.
 *   - Every other CRD is bucketed by its **API group family** (`groupFamily`),
 *     and a family with at least MIN_OPERATOR_KINDS kinds becomes its own
 *     section named after that family — so a Strimzi cluster gets Strimzi and a
 *     Debezium one gets Debezium, on the same rule that used to only produce
 *     Istio.
 *   - Whatever is left — a family with a single kind — lands in **Other**. A
 *     cluster can define a hundred kinds and most belong to one operator's
 *     internals; they are worth reaching, not worth a heading each.
 *
 * Nothing here shadows a fixed entry, because the kinds that would have — the
 * Gateway API's and Istio's — are not declared as fixed entries at all. They live
 * in RICH_CRD_ITEMS and are placed by this function, so they appear exactly when
 * the cluster serves them and still get their proper table.
 */
export function discoverCategories(crds: DiscoveredCRD[]): ResourceCategory[] {
  const networking: ResourceItem[] = []
  const families = new Map<string, ResourceItem[]>()

  for (const crd of crds) {
    const rich = RICH_CRD_ITEMS[`${crd.plural}.${crd.group}`]
    const item = rich ?? customResourceItem(crd)
    if (!item) continue
    if (crd.group === GATEWAY_API_GROUP) {
      networking.push(item)
      continue
    }
    const family = groupFamily(crd.group)
    const bucket = families.get(family)
    if (bucket) bucket.push(item)
    else families.set(family, [item])
  }

  const byLabel = (a: { label: string }, b: { label: string }) => a.label.localeCompare(b.label)

  // Labels are claimed as they are handed out, so a derived name can never
  // collide with a fixed section's ("Cluster") or with another family's — two
  // sections with one name is worse than one section named after its domain.
  const taken = new Set(RESOURCE_CATEGORIES.map((category) => category.label.toLowerCase()))
  const operators: ResourceCategory[] = []
  const other: ResourceItem[] = []

  for (const [family, items] of families) {
    if (items.length < MIN_OPERATOR_KINDS) {
      other.push(...items)
      continue
    }
    const label = familyLabel(family, taken)
    taken.add(label.toLowerCase())
    operators.push({ id: `operator:${family}`, label, items: items.sort(byLabel) })
  }
  operators.sort(byLabel)

  const out: ResourceCategory[] = []
  if (networking.length > 0) {
    out.push({ id: 'networking', label: 'Networking', items: networking.sort(byLabel) })
  }
  out.push(...operators)
  if (other.length > 0) {
    out.push({ id: 'other', label: 'Other', items: other.sort(byLabel) })
  }
  return out
}

/**
 * groupFamily reduces an API group to the thing that installed it. An operator
 * serves several groups under one domain — `kafka.strimzi.io` and
 * `core.strimzi.io`, `networking.istio.io` and `security.istio.io` — so the
 * domain root is what says "these are one area of work", and it is derivable
 * rather than enumerable.
 *
 * The exception is a root that belongs to nobody (SHARED_GROUP_ROOTS): under
 * `k8s.io` the identity is the label in front of it, so one more label is kept.
 */
function groupFamily(group: string): string {
  const labels = group.split('.')
  if (labels.length <= 2) return group
  const root = labels.slice(-2).join('.')
  if (!SHARED_GROUP_ROOTS.has(root)) return root
  return labels.slice(-3).join('.')
}

/**
 * familyLabel names a section after its family: `strimzi.io` → Strimzi,
 * `debezium.io` → Debezium, `kafka.eventing.knative.dev` → Knative. A hyphenated
 * name keeps its hyphen — `cert-manager` is how it is written everywhere else —
 * and a name already in use falls back to the family itself, which is unique by
 * construction.
 */
function familyLabel(family: string, taken: Set<string>): string {
  const override = FAMILY_LABELS[family]
  const derived = override ?? titleCase(family.split('.')[0] ?? family)
  return taken.has(derived.toLowerCase()) ? family : derived
}

function titleCase(name: string): string {
  return name.replace(/(^|-)([a-z])/g, (match) => match.toUpperCase())
}

/**
 * exploreCategories merges the fixed inventory with what a cluster turned out to
 * have. A discovered section carrying a fixed section's id extends it, so the
 * Gateway API's routes sit under Networking with Services and Ingresses.
 *
 * Sections with no fixed counterpart go **below the whole fixed inventory**, the
 * operator sections in the order discovery put them and Other last. However
 * central a mesh or a Kafka operator is to a cluster, it is still a layer over
 * the Pods, Services and Nodes everything else is browsed through — a dozen mesh
 * kinds between Networking and Storage push the core inventory down the column
 * for a section most visits never open.
 */
export function exploreCategories(discovered: ResourceCategory[]): ResourceCategory[] {
  const extra = new Map(discovered.map((category) => [category.id, category]))

  const merged = RESOURCE_CATEGORIES.map((category) => {
    const addition = extra.get(category.id)
    if (!addition) return category
    extra.delete(category.id)
    return { ...category, items: [...category.items, ...addition.items] }
  })

  // What survives, in discovery's order — minus anything a fixed section above
  // already absorbed, and with Other kept last however many operators there are.
  for (const category of discovered) {
    if (extra.has(category.id) && category.id !== 'other') merged.push(category)
  }
  const other = extra.get('other')
  if (other) merged.push(other)
  return merged
}

/**
 * The keys under Access (RBAC) — the lists that describe the *cluster's* own
 * permission model rather than any of its workloads. Derived from the category
 * rather than restated, so adding a kind to that section is one edit.
 *
 * It exists because that section earns something no other does: the access
 * review, which is the only surface in the console that asks the cluster's
 * authorizer a question directly. Showing it over a Pod list would be noise;
 * showing it beside the bindings is showing it where the question is being
 * asked anyway, in the form the cluster will actually answer.
 */
const ACCESS_KEYS = new Set<ResourceKey>(
  RESOURCE_CATEGORIES.find((category) => category.id === 'access')?.items.map(
    (item) => item.key,
  ) ?? [],
)

export function isAccessResource(key: ResourceKey): boolean {
  return ACCESS_KEYS.has(key)
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
