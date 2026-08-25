import { useCallback, useEffect, useMemo, useState } from 'react'
import { Boxes, X } from 'lucide-react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import {
  errorMessage,
  fetchCRDs,
  fetchClusterRoleBindings,
  fetchClusterRoles,
  fetchConfigMaps,
  fetchCustomResources,
  fetchCronJobs,
  fetchDaemonSets,
  fetchDeployments,
  fetchHTTPRoutes,
  fetchHelmReleases,
  fetchIngresses,
  fetchJobs,
  fetchNamespaces,
  fetchNetworkPolicies,
  fetchNodes,
  fetchPersistentVolumeClaims,
  fetchPersistentVolumes,
  fetchPodListMetrics,
  fetchPods,
  fetchRoleBindings,
  fetchRoles,
  fetchSecrets,
  fetchServiceAccounts,
  fetchServices,
  fetchStatefulSets,
  fetchStorageClasses,
  fetchVirtualServices,
  withReadReport,
} from '../api/client'
import type { ReadReport } from '../api/client'
import type { Namespace } from '../api/types'
import { AccessReviewPanel } from '../components/AccessReviewPanel'
import { AppShell } from '../components/AppShell'
import { InsightTrend } from '../components/InsightTrend'
import { LiveRefresh } from '../components/LiveRefresh'
import { NetworkPolicyCoveragePanel } from '../components/NetworkPolicyCoveragePanel'
import { ResourceDetailDrawer } from '../components/ResourceDetailDrawer'
import type { DetailTarget } from '../components/ResourceDetailDrawer'
import { ResourceInsights } from '../components/ResourceInsights'
import { ResourceView } from '../components/ResourceTables'
import { TableSkeleton } from '../components/SkeletonLoader'
import type { LoadedResource } from '../components/ResourceTables'
import {
  Chip,
  EmptyState,
  Notice,
  Pill,
  SearchInput,
  Select,
} from '../components/primitives'
import type { ResourceItem, ResourceKey } from '../lib/resources'
import {
  ALL_NAMESPACES,
  isAccessResource,
  resourceItem,
  resourceSingular,
} from '../lib/resources'
import type { InsightBucket, ResourceInsight } from '../lib/insights'
import {
  bindingInsights,
  bucketLabel as labelForBucket,
  claimInsights,
  configInsights,
  crdInsights,
  cronJobInsights,
  customInsights,
  helmInsights,
  ingressInsights,
  jobInsights,
  matchesPodBucket,
  matchesWorkloadBucket,
  namespaceInsights,
  networkPolicyInsights,
  nodeInsights,
  podInsights,
  roleInsights,
  routeInsights,
  serviceAccountInsights,
  serviceInsights,
  storageClassInsights,
  volumeInsights,
  workloadInsights,
} from '../lib/insights'
import { queryKey, useCachedQuery } from '../lib/query'
import { podUsageIndex } from '../lib/units'
import { workloadKeyFor } from '../lib/workloads'
import { clusterPageHref, resourceHref } from '../lib/navigation'
import { useClusters } from '../state/clusters-context'
import { useInventory } from '../state/inventory-context'

/**
 * loadResource reads one list. The tagged result is what keeps the table
 * rendering honest: the loader decides the shape, and the compiler holds the
 * table to it.
 */
async function loadResource(
  item: ResourceItem,
  clusterId: number,
  namespace: string,
  namespaces: Namespace[],
): Promise<LoadedResource> {
  // A kind discovered from the cluster's own CRDs is read generically, by the
  // API it is served under. There is no case for it below because there cannot
  // be one: the set is different on every cluster.
  if (item.custom) {
    const list = await fetchCustomResources(clusterId, item.custom, namespace)
    return { kind: 'custom', rows: list.items, available: list.available, reason: list.reason }
  }

  switch (item.key) {
    case 'helmreleases':
      return { kind: 'helmreleases', rows: await fetchHelmReleases(clusterId, namespace) }
    case 'pods': {
      // The list and its usage are read together, and the usage read is allowed
      // to come back with nothing: metrics-server is optional and reading
      // metrics.k8s.io is its own permission, so a cluster that cannot answer
      // must still show its pods. `null` is what the table draws a dash for.
      const [rows, metrics] = await Promise.all([
        fetchPods(clusterId, namespace),
        fetchPodListMetrics(clusterId, namespace).catch(() => null),
      ])
      return {
        kind: 'pods',
        rows,
        usage: metrics?.available ? podUsageIndex(metrics.pods) : null,
        usageReason: metrics && !metrics.available ? metrics.reason : undefined,
      }
    }
    case 'deployments':
      return { kind: 'workloads', rows: await fetchDeployments(clusterId, namespace) }
    case 'statefulsets':
      return { kind: 'workloads', rows: await fetchStatefulSets(clusterId, namespace) }
    case 'daemonsets':
      return { kind: 'workloads', rows: await fetchDaemonSets(clusterId, namespace) }
    case 'jobs':
      return { kind: 'jobs', rows: await fetchJobs(clusterId, namespace) }
    case 'cronjobs':
      return { kind: 'cronjobs', rows: await fetchCronJobs(clusterId, namespace) }
    case 'services':
      return { kind: 'services', rows: await fetchServices(clusterId, namespace) }
    case 'ingresses':
      return { kind: 'ingresses', rows: await fetchIngresses(clusterId, namespace) }
    case 'networkpolicies':
      return { kind: 'networkpolicies', rows: await fetchNetworkPolicies(clusterId, namespace) }
    case 'httproutes': {
      const list = await fetchHTTPRoutes(clusterId, namespace)
      return { kind: 'routes', rows: list.items, available: list.available, reason: list.reason }
    }
    case 'virtualservices': {
      const list = await fetchVirtualServices(clusterId, namespace)
      return { kind: 'routes', rows: list.items, available: list.available, reason: list.reason }
    }
    case 'persistentvolumes':
      return { kind: 'persistentvolumes', rows: await fetchPersistentVolumes(clusterId) }
    case 'persistentvolumeclaims':
      return {
        kind: 'persistentvolumeclaims',
        rows: await fetchPersistentVolumeClaims(clusterId, namespace),
      }
    case 'storageclasses':
      return { kind: 'storageclasses', rows: await fetchStorageClasses(clusterId) }
    case 'configmaps':
      return { kind: 'config', rows: await fetchConfigMaps(clusterId, namespace), secrets: false }
    case 'secrets':
      return { kind: 'config', rows: await fetchSecrets(clusterId, namespace), secrets: true }
    // The cluster's own RBAC. Roles and ClusterRoles — and the two binding kinds
    // — share a loaded shape because they share a definition; `clusterScoped` is
    // what the table needs to title itself and to drop a namespace that is not
    // there.
    case 'roles':
      return { kind: 'roles', rows: await fetchRoles(clusterId, namespace), clusterScoped: false }
    case 'clusterroles':
      return { kind: 'roles', rows: await fetchClusterRoles(clusterId), clusterScoped: true }
    case 'rolebindings':
      return {
        kind: 'rolebindings',
        rows: await fetchRoleBindings(clusterId, namespace),
        clusterScoped: false,
      }
    case 'clusterrolebindings':
      return {
        kind: 'rolebindings',
        rows: await fetchClusterRoleBindings(clusterId),
        clusterScoped: true,
      }
    case 'serviceaccounts':
      return { kind: 'serviceaccounts', rows: await fetchServiceAccounts(clusterId, namespace) }
    case 'crds':
      return { kind: 'crds', rows: await fetchCRDs(clusterId) }
    case 'nodes':
      return { kind: 'nodes', rows: await fetchNodes(clusterId) }
    case 'namespaces':
      // The namespace list is already loaded for the picker; showing it again
      // would be a second identical read of the cluster.
      return { kind: 'namespaces', rows: namespaces }
    default:
      // Every fixed key is handled above and a discovered one returned earlier,
      // so this is only reachable if the inventory and this loader disagree.
      throw new Error(`kubemg cannot read ${item.key}`)
  }
}

/**
 * How many columns a resource's table has, for the skeleton drawn before the
 * first answer arrives. It is an approximation on purpose — a skeleton is
 * scaffolding, and one column out is invisible — but it is close enough that the
 * table does not visibly resize when the rows land, which is the whole point of
 * drawing one.
 */
const SKELETON_COLUMNS: Partial<Record<string, number>> = {
  pods: 6,
  deployments: 5,
  statefulsets: 5,
  daemonsets: 5,
  jobs: 5,
  cronjobs: 5,
  services: 5,
  ingresses: 4,
  networkpolicies: 5,
  nodes: 5,
  persistentvolumes: 5,
  persistentvolumeclaims: 5,
  configmaps: 4,
  secrets: 4,
  helmreleases: 5,
  namespaces: 3,
  roles: 5,
  clusterroles: 5,
  rolebindings: 5,
  clusterrolebindings: 5,
  serviceaccounts: 5,
}

function skeletonColumns(item: ResourceItem, showNamespace: boolean): number {
  // The extra column is the namespace one, so it is counted only where the table
  // will actually draw it: a cluster-scoped kind has no namespace whatever the
  // selector above the list says.
  const namespaceColumn = showNamespace && item.scope === 'namespaced'
  return (SKELETON_COLUMNS[item.key] ?? 4) + (namespaceColumn ? 1 : 0)
}

/**
 * The object filter narrows a loaded list to rows whose name matches — the
 * gap the sidebar's own filter cannot close, since that one searches resource
 * *kinds* rather than the hundreds of objects inside one. Written as an
 * exhaustive switch rather than a generic cast: every `LoadedResource` variant
 * carries a `name` on its rows, but the union is what keeps the tag and the
 * filtered rows from drifting apart under a cast.
 *
 * A namespaced kind matches on its namespace too (`qualified`), because the
 * namespace is a column of the list now: typing `monitoring` into a filter
 * sitting above a visible Namespace column has to narrow to that namespace, and
 * an all-namespaces list is exactly where somebody needs it to. A cluster-scoped
 * kind has no namespace to match, which is why the two matchers stay separate
 * rather than one reaching for a field half these row types do not have.
 */
function filterLoaded(loaded: LoadedResource, needle: string): LoadedResource {
  if (!needle) return loaded
  const match = (name: string) => name.toLowerCase().includes(needle)
  const qualified = (row: { name: string; namespace?: string }) =>
    match(row.name) || (!!row.namespace && match(row.namespace))
  switch (loaded.kind) {
    case 'pods':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'helmreleases':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'workloads':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'jobs':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'cronjobs':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'services':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'ingresses':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'networkpolicies':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'routes':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'persistentvolumes':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'persistentvolumeclaims':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'storageclasses':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'config':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'crds':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'roles':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'rolebindings':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'serviceaccounts':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'custom':
      return { ...loaded, rows: loaded.rows.filter(qualified) }
    case 'nodes':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'namespaces':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
  }
}

/**
 * The pilot header for whichever list is loaded. Every kind earns one now — a
 * band that appeared over two lists and vanished over the other fourteen made
 * the page change shape with every click in the tree, so the eye had no fixed
 * place to look. What differs per kind is not whether there is a header but what
 * is honest to put in it: pods and workloads have a state to report, a
 * ConfigMap list has a composition and a count, and `lib/insights.ts` is where
 * that distinction is drawn rather than here.
 */
function insightsFor(
  loaded: LoadedResource | null | undefined,
  label: string,
): ResourceInsight | null {
  if (!loaded) return null
  switch (loaded.kind) {
    case 'pods':
      return podInsights(loaded.rows, loaded.usage, label)
    case 'workloads':
      return workloadInsights(loaded.rows, label)
    case 'jobs':
      return jobInsights(loaded.rows, label)
    case 'cronjobs':
      return cronJobInsights(loaded.rows, label)
    case 'services':
      return serviceInsights(loaded.rows, label)
    case 'ingresses':
      return ingressInsights(loaded.rows, label)
    case 'networkpolicies':
      return networkPolicyInsights(loaded.rows, label)
    case 'routes':
      return routeInsights(loaded.rows, label)
    case 'helmreleases':
      return helmInsights(loaded.rows, label)
    case 'persistentvolumes':
      return volumeInsights(loaded.rows, label)
    case 'persistentvolumeclaims':
      return claimInsights(loaded.rows, label)
    case 'storageclasses':
      return storageClassInsights(loaded.rows, label)
    case 'config':
      return configInsights(loaded.rows, label, loaded.secrets)
    case 'crds':
      return crdInsights(loaded.rows, label)
    case 'roles':
      return roleInsights(loaded.rows, label, loaded.clusterScoped)
    case 'rolebindings':
      return bindingInsights(loaded.rows, label, loaded.clusterScoped)
    case 'serviceaccounts':
      return serviceAccountInsights(loaded.rows, label)
    case 'custom':
      return customInsights(loaded.rows, label)
    case 'nodes':
      return nodeInsights(loaded.rows, label)
    case 'namespaces':
      return namespaceInsights(loaded.rows, label)
  }
}

/**
 * Narrows a loaded list to one of the header's buckets — clicking a reading is
 * a request for the rows behind it, and the alternative would be reading the
 * count and then hunting for them by hand. Applied after the name filter, so
 * the two compose: "the failing pods whose name contains api".
 */
function narrowToBucket(loaded: LoadedResource, bucket: InsightBucket | null): LoadedResource {
  if (!bucket) return loaded
  if (loaded.kind === 'pods') {
    return { ...loaded, rows: loaded.rows.filter((row) => matchesPodBucket(row, bucket)) }
  }
  if (loaded.kind === 'workloads') {
    return { ...loaded, rows: loaded.rows.filter((row) => matchesWorkloadBucket(row, bucket)) }
  }
  return loaded
}

/**
 * The namespace an operator last chose, kept across sessions. Someone working in
 * one namespace goes back to it every time they open Explore, and re-picking it
 * on every visit is the kind of friction a console should absorb. It is stored
 * as a single preference rather than one per cluster because it is a habit, not
 * a property of a cluster — and it is only restored where it is valid, so a
 * cluster without that namespace simply opens on its first one.
 */
const NAMESPACE_KEY = 'kubemg_preferred_namespace'

/** The namespace query parameter — what puts "the Services in `payments`" into
    a link, distinct from the remembered habit in `NAMESPACE_KEY`, which is
    what a bare `/explore` falls back to. */
const NS_PARAM = 'ns'

/** What Explore opens on, and what it falls back to when a selection goes away. */
const DEFAULT_ITEM = resourceItem('pods')!

/**
 * The Kubernetes Kind behind a list, for narrowing the events timeline to one
 * object. `singular` is what the inventory already carries for exactly this
 * purpose — a list's label is a plural, and an event's `involvedObject.kind` is
 * not.
 *
 * It returns an empty string wherever the Kind is not certain rather than
 * guessing: the timeline accepts a name with no kind (two kinds rarely share a
 * name), and narrowing to the wrong Kind would show an empty timeline for an
 * object that has events, which is worse than a slightly wider one.
 */
function alertKind(item: ResourceItem): string {
  // A Helm release is the one entry whose singular is not a Kubernetes Kind:
  // there is no `Release` object, only the Secret holding it, so narrowing by it
  // would produce an empty timeline for a release that has events under the
  // names of everything it installed.
  if (item.key === 'helmreleases') return ''
  // A discovered CRD's singular *is* its Kind — the inventory reads it off the
  // CRD — so those narrow correctly without a special case.
  const singular = item.singular ?? (item.label.endsWith('s') ? item.label.slice(0, -1) : '')
  // Only a Kind-shaped word: the timeline validates this server-side and would
  // refuse anything else, but sending a refusable link is a broken link.
  return /^[A-Za-z][A-Za-z0-9]*$/.test(singular) ? singular : ''
}

/** A link into one cluster's events timeline, narrowed to one object. */
function eventsHref(
  clusterId: number,
  namespace: string,
  kind: string,
  name: string,
): string {
  const params = new URLSearchParams()
  params.set('ns', namespace || ALL_NAMESPACES)
  // A kind is only sent with a name, which is the pairing the server accepts:
  // narrowing to a kind alone is what the namespace scope already does.
  if (kind && name) params.set('kind', kind)
  if (name) params.set('name', name)
  return `/clusters/${clusterId}/events?${params.toString()}`
}

function readPreferredNamespace(): string {
  try {
    return localStorage.getItem(NAMESPACE_KEY) ?? ''
  } catch {
    // Private-mode storage refusals are not worth breaking a page over.
    return ''
  }
}

function writePreferredNamespace(namespace: string) {
  try {
    localStorage.setItem(NAMESPACE_KEY, namespace)
  } catch {
    /* ignored, as above */
  }
}

/**
 * The cluster id in `/clusters/:id/explore/*`, or null when Explore is
 * mounted without one — which only happens at the bare `/explore` landing,
 * when the whole fleet has nothing reachable to redirect to (see
 * `ExploreLanding` in `App.tsx`, which owns the redirect itself).
 */
function readClusterParam(raw: string | undefined): number | null {
  if (!raw) return null
  const id = Number(raw)
  return Number.isInteger(id) && id > 0 ? id : null
}

/**
 * The resource key is the splat tail of `/clusters/:id/explore/*` — a splat
 * rather than a plain `:kind` segment because a discovered CRD's key
 * (`crd:group/version/plural`) carries slashes of its own that a single path
 * segment cannot hold. An empty tail (`/explore` before the redirect lands)
 * reads the same as no selection at all.
 */
function readResourceParam(raw: string | undefined): ResourceKey | null {
  return raw ? (raw as ResourceKey) : null
}

export function Explore() {
  const { clusters, loading: clustersLoading } = useClusters()
  const navigate = useNavigate()

  // The cluster, the resource and the namespace being explored are all named
  // in the address — "the Services in `payments` on `prod-eu-west-1`" is a
  // link, not a sequence of clicks to reproduce. The cluster and the resource
  // live in the path (the fleet list and the sidebar navigate rather than set
  // state), the namespace in the query string beside it.
  const params = useParams<{ id: string; '*': string }>()
  const clusterId = readClusterParam(params.id)
  const resourceParam = readResourceParam(params['*'])
  const resource: ResourceKey = resourceParam ?? 'pods'

  const [searchParams, setSearchParams] = useSearchParams()
  const namespace = searchParams.get(NS_PARAM) ?? ''

  const [namespaces, setNamespaces] = useState<Namespace[]>([])
  const [scoped, setScoped] = useState(false)
  // Narrows the loaded list to matching names — the sidebar's own filter
  // searches resource *kinds*, and this is the gap it leaves: nothing in it
  // can find one pod among two hundred.
  const [objectFilter, setObjectFilter] = useState('')
  // Which of the pilot header's readings the list is narrowed to, if any. It is
  // page state rather than an address parameter: a name filter and a bucket are
  // both a way of reading one list, and neither is worth a link the way the
  // cluster, the resource and the namespace are.
  const [bucket, setBucket] = useState<InsightBucket | null>(null)

  // One drawer for every kind and every action, opened on whichever tab or
  // panel the row asked for. A pod and a Helm release each carry their row
  // along, because the list already holds what their panels need without a
  // second read; a workload carries the write it was asked for, which opens as
  // a panel inside the drawer rather than as a second surface over it.
  const [detail, setDetail] = useState<DetailTarget | null>(null)

  // Listing namespaces is its own read with its own failure — a grant can browse
  // a cluster it cannot enumerate — so it keeps its own error rather than
  // sharing the list's.
  const [namespaceError, setNamespaceError] = useState<string | null>(null)

  // What this particular cluster turned out to have installed, read once per
  // cluster by the shell — the tree is drawn on every one of a cluster's pages,
  // so the inventory has to outlive any one of them. `ready` is false while the
  // question is still open, which is different from "none": a discovered
  // resource cannot be resolved until it is settled.
  const { discovered, categories, ready: inventoryReady } = useInventory()

  // Only agent clusters have a tunnel to read through; a direct-mode cluster has
  // no live state to show.
  const reachable = useMemo(
    () => clusters.filter((entry) => entry.connection_mode === 'agent' && entry.agent_attached),
    [clusters],
  )
  const cluster = reachable.find((entry) => entry.id === clusterId) ?? null
  // A cluster that is registered but cannot be read: the address is honoured and
  // explained rather than quietly swapped for a different cluster's resources.
  const unreadable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  // A `crd:` key belongs to the cluster it was discovered on, so it resolves to
  // nothing both while discovery is still running and on a cluster that does not
  // have that CRD. Those are different situations: the first waits, the second
  // falls back — see the effect below.
  const resolved = resourceItem(resource, discovered)
  const item = resolved ?? DEFAULT_ITEM
  const namespaced = item.scope === 'namespaced'

  /** Navigates to a different resource on the same cluster, carrying the
      current query string (the namespace) along — a click in the tree is a
      navigation, not a `setState`. Memoised so the effect below, which falls
      an unresolved selection back to Pods, has a stable function to depend on
      rather than re-running on every render. */
  const selectResource = useCallback(
    (key: ResourceKey, replace = false) => {
      if (!cluster) return
      const qs = searchParams.toString()
      navigate(resourceHref(cluster.id, key, qs), { replace })
    },
    [cluster, searchParams, navigate],
  )

  function selectNamespace(next: string) {
    setSearchParams(
      (previous) => {
        const params = new URLSearchParams(previous)
        params.set(NS_PARAM, next)
        return params
      },
      { replace: true },
    )
    writePreferredNamespace(next)
  }

  // Namespaces reload whenever the cluster changes; the current namespace is
  // resolved fresh so it cannot leak across clusters.
  useEffect(() => {
    if (!cluster) return

    let cancelled = false
    setNamespaces([])
    setNamespaceError(null)

    fetchNamespaces(cluster.id)
      .then((result) => {
        if (cancelled) return
        setNamespaces(result.namespaces)
        setScoped(result.scoped)
        if (result.namespaces.length === 0) return

        // The address wins where it names a namespace this cluster actually
        // has; otherwise the remembered habit applies where it still means
        // something here ("all" always does, a named namespace only if this
        // cluster has it and the grant returned it), and failing that, the
        // first one. Read through `setSearchParams`'s functional form rather
        // than the outer `searchParams`, so this effect depends on nothing
        // that changes on every namespace pick — only the cluster.
        setSearchParams(
          (previous) => {
            const requested = previous.get(NS_PARAM) ?? ''
            const preferred = requested || readPreferredNamespace()
            const valid =
              preferred === ALL_NAMESPACES ||
              result.namespaces.some((entry) => entry.name === preferred)
            const next = valid ? preferred : result.namespaces[0].name
            if (next === requested) return previous
            const params = new URLSearchParams(previous)
            params.set(NS_PARAM, next)
            return params
          },
          { replace: true },
        )
      })
      .catch((err) => {
        if (!cancelled) setNamespaceError(errorMessage(err, 'Could not list namespaces.'))
      })

    return () => {
      cancelled = true
    }
  }, [cluster, setSearchParams])

  // Once discovery has answered, a selection it could not account for belongs to
  // a cluster that is no longer open. Dropping it back to Pods keeps the sidebar
  // highlight, the heading and the list describing the same thing.
  useEffect(() => {
    if (inventoryReady && !resolved) selectResource('pods', true)
  }, [inventoryReady, resolved, selectResource])

  /*
   * The list is read through the query cache, which is what makes the sidebar
   * feel like one surface: a click to Services and straight back to Pods is two
   * navigations and no cluster reads, because the answer is seconds old and
   * still on hand. A null key means there is nothing to read yet — no reachable
   * cluster, no namespace resolved, or a discovered kind whose discovery has not
   * come back — and reading the fallback there would show Pods under another
   * resource's heading for as long as that takes.
   */
  const readKey =
    cluster && resolved && (!namespaced || namespace)
      ? queryKey(
          'resources',
          cluster.id,
          item.key,
          namespaced ? namespace : '-',
          // The namespace list is the one resource read from state rather than
          // from the cluster, so its answer changes when that state arrives.
          item.key === 'namespaces' ? namespaces.length : '',
        )
      : null

  const list = useCachedQuery<{ resource: LoadedResource; report: ReadReport }>(
    readKey,
    async () => {
      // Unreachable while the key is null, which is the only state without a
      // cluster; it is written as a guard rather than an assertion so the two
      // cannot drift apart silently.
      if (!cluster) throw new Error('no cluster is selected')
      // The read is wrapped so a list the server had to bound arrives with the
      // bound attached rather than presenting itself as the whole cluster.
      const { value, report } = await withReadReport(() =>
        loadResource(item, cluster.id, namespace, namespaces),
      )
      return { resource: value, report }
    },
    // The list keeps itself true. A rollout, a crash loop or a scale-down is
    // exactly what somebody has this page open to watch, and a table that
    // silently describes the cluster as it was a minute ago taught people to
    // reach for Refresh instead of trusting it. The tick is invisible: nothing
    // is redrawn unless the answer actually changed, and it stops the moment
    // the tab does.
    { live: true },
  )

  const loaded = list.data?.resource ?? null
  const truncatedAt = list.data?.report.truncatedAt ?? null
  // Anything in flight counts as loading for the header's live note; only
  // `list.loading` — nothing on screen yet — is what draws a skeleton.
  const loading = list.loading || list.revalidating
  const error =
    namespaceError ??
    (list.error
      ? errorMessage(list.error, `Could not read ${item.label.toLowerCase()} from this cluster.`)
      : null)
  // Refresh, and what a write comes back to: it skips this cache and the
  // server's, so it really does ask the cluster.
  const load = list.refresh

  // A drawer belongs to the list it was opened from; switching resources closes
  // it rather than leaving it open over a list it does not come from. The
  // object filter is scoped to one list the same way — a name that matched in
  // Pods says nothing about Services.
  useEffect(() => {
    setDetail(null)
    setObjectFilter('')
    setBucket(null)
  }, [resource, namespace, clusterId])

  if (!clustersLoading && reachable.length === 0) {
    return (
      <AppShell title={item.label}>
        <div className="card">
          <EmptyState
            icon={<Boxes aria-hidden="true" className="size-5" />}
            title="No cluster is reachable right now"
          >
            Live resources are read on demand through an agent tunnel. Register a cluster in agent
            mode and wait for its agent to dial in.
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  // The address names a cluster whose resources cannot be read. Both shapes are
  // the same answer — nothing to explore here — and both point at the cluster's
  // own page, which is where its connection is managed.
  if (!clustersLoading && clusterId !== null && !cluster) {
    return (
      <AppShell title={item.label}>
        <div className="card">
          <EmptyState
            icon={<Boxes aria-hidden="true" className="size-5" />}
            title={
              unreadable
                ? `${unreadable.name} has no live connection`
                : 'That cluster is not registered'
            }
          >
            {unreadable ? (
              <>
                {unreadable.connection_mode === 'agent'
                  ? 'Its agent has not dialled in, so there is no tunnel to read through. '
                  : 'It is registered in direct mode, which has no agent tunnel for live reads. '}
                <Link
                  to={clusterPageHref(unreadable.id, 'dashboard')}
                  className="text-accent hover:underline"
                >
                  Open the cluster
                </Link>{' '}
                to check its connection, or pick another cluster from the fleet list.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read its resources.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const unavailable = (loaded?.kind === 'routes' || loaded?.kind === 'custom') && !loaded.available
  const needle = objectFilter.trim().toLowerCase()
  const filtered = loaded ? narrowToBucket(filterLoaded(loaded, needle), bucket) : loaded
  const totalCount = loaded?.rows.length ?? 0
  const count = filtered?.rows.length ?? 0
  const allNamespaces = namespaced && namespace === ALL_NAMESPACES

  // The header summarises the whole list, not the narrowed one: it is what the
  // narrowing is chosen *from*, so recomputing it against its own selection
  // would collapse every reading but the selected one to zero.
  const insight = insightsFor(loaded, item.label)

  /*
   * Whether the band earns a trend region. Three conditions, and each is a
   * different kind of "no":
   *
   *   - the list has to be one whose objects consume something. Pods and the
   *     workloads that own them do; a ConfigMap list charting namespace CPU
   *     would be a chart about somebody else's objects.
   *   - exactly one namespace has to be selected. The catalogue's namespaced
   *     entries are per-namespace, and there is no honest all-namespaces
   *     equivalent — a cluster-wide curve is refused outright to a scoped grant,
   *     and summing every namespace answers a question this list did not ask. So
   *     "All namespaces" keeps the simple band.
   *   - and there has to be a cluster to read through.
   *
   * A cluster with no datasource is deliberately *not* on that list: the region
   * appears and says so, because a band that changes shape depending on which
   * cluster is open is the thing this header was rebuilt to stop.
   */
  const charts = loaded?.kind === 'pods' || loaded?.kind === 'workloads'
  const trend =
    cluster && charts && namespaced && namespace && !allNamespaces ? (
      <InsightTrend
        cluster={cluster}
        namespace={namespace}
        onConfigure={() => navigate(clusterPageHref(cluster.id, 'dashboard'))}
      />
    ) : undefined
  // The active narrowing named in the list header, so a reading clicked at the
  // top of the page is still explained after scrolling down to the rows — and
  // so there is somewhere to undo it that is not back up there.
  const bucketLabel = labelForBucket(insight, bucket)

  return (
    <AppShell
      // The kind *is* the page now: the tree is the navigation, and this is the
      // leaf it landed on. "Explore" as a page title would name a destination
      // that no longer exists in the chrome.
      title={item.label}
      fullWidth
      timeRange
      scope={
        // Namespace is a scope control of the same class as the cluster and
        // the time range, so it sits beside them in the header rather than in
        // the page body; it goes quiet on a cluster-scoped kind, since there
        // is nothing for it to narrow.
        cluster && namespaced ? (
          <div className="w-44">
            <Select
              aria-label="Namespace"
              size="sm"
              value={namespace}
              disabled={namespaces.length === 0}
              onChange={(event) => selectNamespace(event.target.value)}
            >
              {namespaces.length === 0 ? <option value="">No namespaces</option> : null}
              {/* A scoped grant's "all" is its own namespaces, and it says so
                  rather than implying it covers the cluster. */}
              {namespaces.length > 0 ? (
                <option value={ALL_NAMESPACES}>
                  {scoped ? 'All granted namespaces' : 'All namespaces'}
                </option>
              ) : null}
              {namespaces.map((entry) => (
                <option key={entry.name} value={entry.name}>
                  {entry.name}
                </option>
              ))}
            </Select>
          </div>
        ) : undefined
      }
      actions={<LiveRefresh query={list} disabled={namespaced && !namespace} />}
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {/* A bounded read says so. Every reading on this page — the pilot
            header's buckets, the counts, the filter — is derived from the rows
            that arrived, so a table quietly holding the first two thousand of a
            cluster's twelve thousand would make all of them wrong without
            looking wrong. The way out is a narrower question, which is what the
            note asks for. */}
        {truncatedAt !== null ? (
          <Notice tone="warn">
            This cluster has more {item.label.toLowerCase()} than one read returns, so this is the
            first {truncatedAt.toLocaleString()} of them and everything on this page describes only
            those. Pick a single namespace, or filter by name, to see a complete answer.
          </Notice>
        ) : null}

        {/* The tree survives down to `lg` as the section panel's own content;
            below that all chrome collapses into the mobile sheet, so the
            resource is picked here instead. */}
        <div className="lg:hidden">
          <Select
            aria-label="Resource"
            size="sm"
            value={resource}
            onChange={(event) => selectResource(event.target.value as ResourceKey)}
          >
            {categories.map((category) => (
              <optgroup key={category.id} label={category.label}>
                {category.items.map((entry) => (
                  <option key={entry.key} value={entry.key}>
                    {entry.label}
                  </option>
                ))}
              </optgroup>
            ))}
          </Select>
        </div>

        {/* The access review, over the RBAC lists and nowhere else. The lists
            below it are what is *written down*; this is the authorizer's own
            verdict, which is the part reading a binding table cannot give — and
            it belongs beside those tables rather than on a page of its own,
            because the question is asked while looking at them. */}
        {cluster && isAccessResource(item.key) ? (
          <AccessReviewPanel
            cluster={cluster}
            // A cluster-scoped list has no namespace picker above it, so the
            // question it frames is a cluster-wide one — asking it in whichever
            // namespace the query string still carried would be answering a
            // question this page is not showing.
            namespace={namespaced ? namespace : ALL_NAMESPACES}
          />
        ) : null}

        {/* The namespace-level half of the NetworkPolicy feature: what is and
            is not covered, over the list of the policies themselves. A
            NetworkPolicy never crosses a namespace boundary, so this only
            answers something for one namespace at a time — it stays quiet
            under "All namespaces" rather than guessing at a rollup nothing
            here actually computes. */}
        {cluster && item.key === 'networkpolicies' && namespace && namespace !== ALL_NAMESPACES ? (
          <NetworkPolicyCoveragePanel cluster={cluster} namespace={namespace} />
        ) : null}

        {/* The pilot header. It sits above the list rather than inside it
            because it describes the whole list, including the rows a filter or
            a bucket is currently hiding — and it is drawn only where there is a
            list to describe: over an empty one it would restate the empty state
            below it in bigger type. */}
        {insight && !unavailable && totalCount > 0 ? (
          <ResourceInsights
            insight={insight}
            bucket={bucket}
            onBucket={setBucket}
            trend={trend}
            // The next question after the header names something: what has the
            // cluster actually been saying about it. The header can raise
            // `CrashLoopBackOff` because a container status says so, but only the
            // events say why — and they were reachable until now only by opening
            // the object and finding the right tab.
            alertHref={
              cluster
                ? (alert) => eventsHref(cluster.id, alert.namespace, alertKind(item), alert.name)
                : undefined
            }
            onOpen={(name, rowNamespace) =>
              setDetail({
                kind: resource,
                label: resourceSingular(item),
                name,
                // A cluster-scoped kind's alerts carry no namespace, and the
                // drawer must not be handed an empty one — it addresses an
                // object by the same rule the tables do.
                namespace: namespaced ? rowNamespace : undefined,
                // A pod's own row carries everything its panels need, so an
                // alert opens the drawer without a second read.
                pod:
                  loaded?.kind === 'pods'
                    ? loaded.rows.find(
                        (row) => row.name === name && row.namespace === rowNamespace,
                      )
                    : undefined,
              })
            }
          />
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">{item.label}</h2>
            <span className="font-mono text-[12.5px] text-faint">
              {count !== totalCount ? `${count} of ${totalCount}` : count}
            </span>
            {namespaced && namespace ? (
              <span className="text-[12.5px] text-muted">
                {allNamespaces ? (
                  scoped ? (
                    'across your granted namespaces'
                  ) : (
                    'across every namespace'
                  )
                ) : (
                  <>
                    in <span className="font-mono">{namespace}</span>
                  </>
                )}
              </span>
            ) : null}
            {!namespaced ? (
              <Pill tone="idle" dot={false}>
                cluster-scoped
              </Pill>
            ) : null}
            {scoped && namespaced ? (
              <Pill tone="accent" dot={false}>
                scoped to your grant
              </Pill>
            ) : null}
            {bucketLabel ? (
              <Chip active onClick={() => setBucket(null)}>
                {bucketLabel}
                <X aria-hidden="true" className="size-3.5" />
                <span className="sr-only">Show every {item.label.toLowerCase()}</span>
              </Chip>
            ) : null}
            {loading ? <span className="text-[12px] text-muted">Reading the cluster…</span> : null}
            {totalCount > 0 ? (
              <SearchInput
                value={objectFilter}
                onChange={setObjectFilter}
                placeholder={`Filter ${item.label.toLowerCase()}`}
                label={`Filter ${item.label.toLowerCase()}`}
                className="ml-auto w-full sm:w-56"
              />
            ) : null}
          </div>

          {unavailable ? (
            <div className="p-4">
              <Notice tone="info">
                {loaded.reason ?? 'This cluster does not serve that resource.'}
              </Notice>
            </div>
          ) : null}

          {/* A column of dashes needs one line saying why, and it is a line
              rather than a notice: metrics-server is missing on plenty of
              clusters and the pod list is still the thing being read. */}
          {loaded?.kind === 'pods' && loaded.usageReason && count > 0 ? (
            <p className="border-b border-line-soft px-4 py-2 text-[12px] text-muted">
              {loaded.usageReason}
            </p>
          ) : null}

          {filtered && !unavailable ? (
            <ResourceView
              loaded={filtered}
              showNamespace={allNamespaces}
              // A pod opens the same drawer as everything else, but carrying its
              // row: the list already holds the containers and limits its usage
              // panel needs, so there is nothing to read again.
              onSelectPod={(pod) =>
                setDetail({
                  kind: 'pods',
                  label: 'Pod',
                  name: pod.name,
                  namespace: pod.namespace,
                  pod,
                })
              }
              // A release opens the same drawer too, carrying its row: it has
              // no manifest and no describe, so its two tabs are its values and
              // the revisions behind them, but reaching it is the same motion as
              // reaching anything else.
              onValues={(release, tab, editing) =>
                setDetail({
                  kind: 'helmreleases',
                  label: 'Helm release',
                  name: release.name,
                  namespace: release.namespace,
                  release,
                  tab,
                  editing,
                })
              }
              // A workload row carries its own Kind and its own desired count,
              // which is everything the action panel needs — no second read to
              // open it, and no guess about what "currently" is. It opens the
              // object as well as the write: acting on something without seeing
              // it is what the old stacked dialog got wrong.
              onAction={(name, workload) => {
                const kind = workloadKeyFor(workload.kind)
                if (!kind) return
                setDetail({
                  kind,
                  label: workload.kind,
                  name: workload.name,
                  namespace: workload.namespace,
                  action: name,
                })
              }}
              onManifest={(name, rowNamespace, tab, editing) =>
                setDetail({
                  kind: resource,
                  label: resourceSingular(item),
                  name,
                  namespace: namespaced ? (rowNamespace ?? namespace) : undefined,
                  pod:
                    loaded?.kind === 'pods'
                      ? loaded.rows.find((row) => row.name === name)
                      : undefined,
                  tab,
                  editing,
                })
              }
            />
          ) : null}

          {!loading && namespaced && !namespace ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              Select a namespace to read {item.label.toLowerCase()}.
            </p>
          ) : null}

          {!loading && !unavailable && loaded && count === 0 && needle ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              Nothing matches “{objectFilter}”
              {bucketLabel ? <> under {bucketLabel.toLowerCase()}</> : null}.
            </p>
          ) : null}

          {!loading && !unavailable && loaded && count === 0 && !needle && bucketLabel ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              No {item.label.toLowerCase()} under {bucketLabel.toLowerCase()}.
            </p>
          ) : null}

          {!loading && !unavailable && loaded && count === 0 && !needle && !bucketLabel ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              No {item.label.toLowerCase()}
              {allNamespaces || !namespaced ? (
                ' in this cluster'
              ) : (
                <>
                  {' '}
                  in <span className="font-mono">{namespace}</span>
                </>
              )}
              .
            </p>
          ) : null}

          {/* Nothing has ever been drawn for this question, so the wait is spent
              showing what is coming rather than a spinner in an empty panel. A
              list that is already on screen is left alone while it is re-read —
              replacing it with its own outline would be a regression. */}
          {list.loading ? (
            <TableSkeleton
              columns={skeletonColumns(item, allNamespaces)}
              rows={8}
              label={`Reading ${item.label.toLowerCase()} from ${cluster?.name ?? 'the cluster'}`}
            />
          ) : null}
        </div>

        <p className="text-[12px] text-muted">
          Read live through the agent tunnel under your own identity — the cluster&rsquo;s RBAC
          decides what comes back, and every read is in the audit trail. Secrets are listed as
          metadata only: no value leaves the cluster.
        </p>
      </div>

      {detail && cluster ? (
        <ResourceDetailDrawer
          cluster={cluster}
          target={detail}
          onClose={() => setDetail(null)}
          onRefresh={load}
          onOpen={setDetail}
        />
      ) : null}
    </AppShell>
  )
}
