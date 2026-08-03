import { useCallback, useEffect, useMemo, useState } from 'react'
import { Boxes, RefreshCw } from 'lucide-react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import {
  errorMessage,
  fetchCRDs,
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
  fetchNodes,
  fetchPersistentVolumeClaims,
  fetchPersistentVolumes,
  fetchPodListMetrics,
  fetchPods,
  fetchSecrets,
  fetchServices,
  fetchStatefulSets,
  fetchStorageClasses,
  fetchVirtualServices,
} from '../api/client'
import type { CustomResourceDefinition, HelmRelease, Namespace } from '../api/types'
import { AppShell } from '../components/AppShell'
import { ExploreSidebar } from '../components/ExploreSidebar'
import { HelmValuesDrawer } from '../components/HelmValuesDrawer'
import { ResourceDetailDrawer } from '../components/ResourceDetailDrawer'
import type { DetailTarget } from '../components/ResourceDetailDrawer'
import { ResourceView } from '../components/ResourceTables'
import { TableSkeleton } from '../components/SkeletonLoader'
import type { LoadedResource } from '../components/ResourceTables'
import { WorkloadActionDialog } from '../components/WorkloadActionDialog'
import type { WorkloadActionTarget } from '../components/WorkloadActionDialog'
import {
  Button,
  EmptyState,
  Notice,
  Pill,
  SearchInput,
  Select,
} from '../components/primitives'
import type { ResourceItem, ResourceKey } from '../lib/resources'
import {
  ALL_NAMESPACES,
  discoverCategories,
  exploreCategories,
  resourceItem,
  resourceSingular,
} from '../lib/resources'
import { queryKey, useCachedQuery } from '../lib/query'
import { podUsageIndex } from '../lib/units'
import { workloadKeyFor } from '../lib/workloads'
import { useClusters } from '../state/clusters-context'

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
  nodes: 5,
  persistentvolumes: 5,
  persistentvolumeclaims: 5,
  configmaps: 4,
  secrets: 4,
  helmreleases: 5,
  namespaces: 3,
}

function skeletonColumns(item: ResourceItem, showNamespace: boolean): number {
  return (SKELETON_COLUMNS[item.key] ?? 4) + (showNamespace ? 1 : 0)
}

/**
 * The object filter narrows a loaded list to rows whose name matches — the
 * gap the sidebar's own filter cannot close, since that one searches resource
 * *kinds* rather than the hundreds of objects inside one. Written as an
 * exhaustive switch rather than a generic cast: every `LoadedResource` variant
 * carries a `name` on its rows, but the union is what keeps the tag and the
 * filtered rows from drifting apart under a cast.
 */
function filterLoaded(loaded: LoadedResource, needle: string): LoadedResource {
  if (!needle) return loaded
  const match = (name: string) => name.toLowerCase().includes(needle)
  switch (loaded.kind) {
    case 'pods':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'helmreleases':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'workloads':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'jobs':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'cronjobs':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'services':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'ingresses':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'routes':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'persistentvolumes':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'persistentvolumeclaims':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'storageclasses':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'config':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'crds':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'custom':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'nodes':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
    case 'namespaces':
      return { ...loaded, rows: loaded.rows.filter((row) => match(row.name)) }
  }
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

  // One drawer for every kind, opened on whichever tab the row's action asked
  // for. A pod carries its row along, because the list already holds everything
  // its usage and container panels need without a second read.
  const [detail, setDetail] = useState<DetailTarget | null>(null)
  // A Helm release is the exception, and has to be: it has no manifest and no
  // describe, because it is not an API object at all — it is a Secret holding a
  // compressed blob, and what is worth reading in it is the values.
  const [helm, setHelm] = useState<{ release: HelmRelease; editing: boolean } | null>(null)
  // Scale and rollout restart, asked for from a row. They are writes, so they
  // are a dialog of their own rather than something a click in a list does.
  const [action, setAction] = useState<WorkloadActionTarget | null>(null)

  // Listing namespaces is its own read with its own failure — a grant can browse
  // a cluster it cannot enumerate — so it keeps its own error rather than
  // sharing the list's.
  const [namespaceError, setNamespaceError] = useState<string | null>(null)

  // What this particular cluster turned out to have installed. `null` means the
  // question has not been answered yet, which is different from "none" — a
  // discovered resource cannot be resolved until it is settled. Reading it is
  // best-effort: a grant that cannot list CRDs still browses everything else, so
  // a refusal narrows the sidebar rather than failing the page.
  const [crds, setCrds] = useState<CustomResourceDefinition[] | null>(null)

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

  const discovered = useMemo(() => discoverCategories(crds ?? []), [crds])
  const categories = useMemo(() => exploreCategories(discovered), [discovered])

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
      navigate(`/clusters/${cluster.id}/explore/${key}${qs ? `?${qs}` : ''}`, { replace })
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

  // Which CRDs the cluster has is read once per cluster, not per list: it is
  // what the sidebar is built from, and it changes only when someone installs an
  // operator.
  useEffect(() => {
    if (!cluster) return

    let cancelled = false
    setCrds(null)

    fetchCRDs(cluster.id)
      .then((result) => {
        if (!cancelled) setCrds(result)
      })
      .catch(() => {
        // A namespace-scoped grant cannot list CRDs cluster-wide, and that is a
        // legitimate answer: the sidebar keeps its fixed inventory.
        if (!cancelled) setCrds([])
      })

    return () => {
      cancelled = true
    }
  }, [cluster])

  // Once discovery has answered, a selection it could not account for belongs to
  // a cluster that is no longer open. Dropping it back to Pods keeps the sidebar
  // highlight, the heading and the list describing the same thing.
  useEffect(() => {
    if (crds !== null && !resolved) selectResource('pods', true)
  }, [crds, resolved, selectResource])

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

  const list = useCachedQuery<LoadedResource>(readKey, async () => {
    // Unreachable while the key is null, which is the only state without a
    // cluster; it is written as a guard rather than an assertion so the two
    // cannot drift apart silently.
    if (!cluster) throw new Error('no cluster is selected')
    return loadResource(item, cluster.id, namespace, namespaces)
  })

  const loaded = list.data
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
    setHelm(null)
    setAction(null)
    setObjectFilter('')
  }, [resource, namespace, clusterId])

  if (!clustersLoading && reachable.length === 0) {
    return (
      <AppShell title="Explore">
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
      <AppShell title="Explore">
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
                  to={`/clusters/${unreadable.id}/summary`}
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
  const filtered = loaded ? filterLoaded(loaded, needle) : loaded
  const totalCount = loaded?.rows.length ?? 0
  const count = filtered?.rows.length ?? 0
  const allNamespaces = namespaced && namespace === ALL_NAMESPACES

  return (
    <AppShell
      title="Explore"
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
      panel={
        <ExploreSidebar categories={categories} selected={resource} onSelect={selectResource} />
      }
      actions={
        <Button onClick={() => void load()} disabled={loading || (namespaced && !namespace)}>
          <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

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

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">{item.label}</h2>
            <span className="font-mono text-[12.5px] text-faint">
              {needle && count !== totalCount ? `${count} of ${totalCount}` : count}
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
              // The page is the only place that knows which resource it asked
              // for: several kinds share a table, and an object has to be
              // addressed by what it actually is.
              onValues={(release, editing) => setHelm({ release, editing })}
              // A workload row carries its own Kind and its own desired count,
              // which is everything the dialog needs — no second read to open
              // it, and no guess about what "currently" is.
              onAction={(name, workload) => {
                const kind = workloadKeyFor(workload.kind)
                if (!kind) return
                setAction({
                  action: name,
                  kind,
                  label: workload.kind,
                  name: workload.name,
                  namespace: workload.namespace,
                  replicas: workload.desired,
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
              Nothing matches “{objectFilter}”.
            </p>
          ) : null}

          {!loading && !unavailable && loaded && count === 0 && !needle ? (
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
        />
      ) : null}

      {action && cluster ? (
        <WorkloadActionDialog
          cluster={cluster}
          target={action}
          onClose={() => setAction(null)}
          onDone={load}
        />
      ) : null}

      {helm && cluster ? (
        <HelmValuesDrawer
          cluster={cluster}
          release={helm.release}
          editing={helm.editing}
          onClose={() => setHelm(null)}
          onApplied={load}
        />
      ) : null}
    </AppShell>
  )
}
