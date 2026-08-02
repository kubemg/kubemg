import { useEffect, useMemo, useState } from 'react'
import { Boxes, RefreshCw } from 'lucide-react'
import { Link, useNavigate, useParams } from 'react-router'
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
  EnvironmentTag,
  Notice,
  Pill,
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
 * The namespace an operator last chose, kept across sessions. Someone working in
 * one namespace goes back to it every time they open Explore, and re-picking it
 * on every visit is the kind of friction a console should absorb. It is stored
 * as a single preference rather than one per cluster because it is a habit, not
 * a property of a cluster — and it is only restored where it is valid, so a
 * cluster without that namespace simply opens on its first one.
 */
const NAMESPACE_KEY = 'kubemg_preferred_namespace'

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

/** The cluster id in `/explore/:clusterId`, or null when the path carries none. */
function readClusterParam(raw: string | undefined): number | null {
  if (!raw) return null
  const id = Number(raw)
  return Number.isInteger(id) && id > 0 ? id : null
}

export function Explore() {
  const { clusters, loading: clustersLoading } = useClusters()
  const navigate = useNavigate()

  // The cluster being explored is the one named in the address. That is what
  // makes the rail's cluster list the way to switch: a click there is a
  // navigation, so the sidebar highlight, the page and the reads cannot disagree
  // — and a link to what someone is looking at carries the cluster with it.
  const clusterId = readClusterParam(useParams().clusterId)

  const [namespaces, setNamespaces] = useState<Namespace[]>([])
  const [namespace, setNamespace] = useState('')
  const [scoped, setScoped] = useState(false)
  const [resource, setResource] = useState<ResourceKey>('pods')

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

  // `/explore` with no cluster named settles on the first readable one and says
  // so in the address, so the sidebar has something to highlight. An id that
  // names a cluster is left alone even when it cannot be read — that case gets an
  // explanation below, not a redirect to someone else's resources.
  useEffect(() => {
    if (clusterId !== null || reachable.length === 0) return
    navigate(`/explore/${reachable[0].id}`, { replace: true })
  }, [clusterId, navigate, reachable])

  // Namespaces reload whenever the cluster changes; the current namespace is
  // dropped so it cannot leak across clusters.
  useEffect(() => {
    if (!cluster) return

    let cancelled = false
    setNamespace('')
    setNamespaces([])
    setNamespaceError(null)

    fetchNamespaces(cluster.id)
      .then((result) => {
        if (cancelled) return
        setNamespaces(result.namespaces)
        setScoped(result.scoped)
        if (result.namespaces.length === 0) return

        // The remembered choice only applies where it means something here:
        // "all" always does, a named namespace only if this cluster has it and
        // the grant returned it.
        const preferred = readPreferredNamespace()
        const valid =
          preferred === ALL_NAMESPACES ||
          result.namespaces.some((entry) => entry.name === preferred)
        setNamespace(valid ? preferred : result.namespaces[0].name)
      })
      .catch((err) => {
        if (!cancelled) setNamespaceError(errorMessage(err, 'Could not list namespaces.'))
      })

    return () => {
      cancelled = true
    }
  }, [cluster])

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
    if (crds !== null && !resolved) setResource('pods')
  }, [crds, resolved])

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
  // it rather than leaving it open over a list it does not come from.
  useEffect(() => {
    setDetail(null)
    setHelm(null)
    setAction(null)
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
                <Link to={`/clusters/${unreadable.id}`} className="text-accent hover:underline">
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
  const count = loaded?.rows.length ?? 0
  const allNamespaces = namespaced && namespace === ALL_NAMESPACES

  return (
    <AppShell
      title="Explore"
      sidebar={
        <ExploreSidebar
          categories={categories}
          cluster={cluster ?? undefined}
          selected={resource}
          onSelect={setResource}
        />
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

        <div className="card flex flex-wrap items-center gap-3 px-4 py-3">
          {/* Which cluster is open is stated here, not chosen here: it is picked
              in the fleet list and carried in the address. The name links to the
              cluster's own page, where its connection and access are managed. */}
          {cluster ? (
            <>
              <Link
                to={`/clusters/${cluster.id}`}
                className="font-mono text-[13px] text-fg transition-colors hover:text-accent"
              >
                {cluster.name}
              </Link>
              <EnvironmentTag environment={cluster.environment} />
            </>
          ) : null}

          {/* The namespace only applies to a namespaced list; for nodes, PVs,
              storage classes and CRDs there is nothing for it to narrow. */}
          {namespaced ? (
            <div className="w-48">
              <Select
                aria-label="Namespace"
                size="sm"
                value={namespace}
                disabled={namespaces.length === 0}
                onChange={(event) => {
                  setNamespace(event.target.value)
                  writePreferredNamespace(event.target.value)
                }}
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
          ) : (
            <Pill tone="idle" dot={false}>
              cluster-scoped
            </Pill>
          )}

          {scoped && namespaced ? (
            <Pill tone="accent" dot={false}>
              scoped to your grant
            </Pill>
          ) : null}

          {/* The resource tree survives down to `lg` by collapsing the section
              panel; below that all chrome is in the mobile sheet, so the
              resource is picked here instead. */}
          <div className="w-52 lg:hidden">
            <Select
              aria-label="Resource"
              size="sm"
              value={resource}
              onChange={(event) => setResource(event.target.value as ResourceKey)}
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
        </div>

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">{item.label}</h2>
            <span className="font-mono text-[12.5px] text-faint">{count}</span>
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
            {loading ? (
              <span className="ml-auto text-[12px] text-muted">Reading the cluster…</span>
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

          {loaded && !unavailable ? (
            <ResourceView
              loaded={loaded}
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
                    loaded.kind === 'pods'
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

          {!loading && !unavailable && loaded && count === 0 ? (
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
