import { useCallback, useEffect, useMemo, useState } from 'react'
import { Boxes, RefreshCw } from 'lucide-react'
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
    case 'pods':
      return { kind: 'pods', rows: await fetchPods(clusterId, namespace) }
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

export function Explore() {
  const { clusters, loading: clustersLoading } = useClusters()

  const [clusterId, setClusterId] = useState<number | null>(null)
  const [namespaces, setNamespaces] = useState<Namespace[]>([])
  const [namespace, setNamespace] = useState('')
  const [scoped, setScoped] = useState(false)
  const [resource, setResource] = useState<ResourceKey>('pods')

  const [loaded, setLoaded] = useState<LoadedResource | null>(null)
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

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // What this particular cluster turned out to have installed. `null` means the
  // question has not been answered yet, which is different from "none" — a
  // discovered resource cannot be resolved until it is settled. Reading it is
  // best-effort: a grant that cannot list CRDs still browses everything else, so
  // a refusal narrows the sidebar rather than failing the page.
  const [crds, setCrds] = useState<CustomResourceDefinition[] | null>(null)

  // Only agent clusters have a tunnel to read through; a direct-mode cluster has
  // no live state to show.
  const reachable = clusters.filter(
    (cluster) => cluster.connection_mode === 'agent' && cluster.agent_attached,
  )
  const cluster = reachable.find((entry) => entry.id === clusterId) ?? null

  const discovered = useMemo(() => discoverCategories(crds ?? []), [crds])
  const categories = useMemo(() => exploreCategories(discovered), [discovered])

  // A `crd:` key belongs to the cluster it was discovered on, so it resolves to
  // nothing both while discovery is still running and on a cluster that does not
  // have that CRD. Those are different situations: the first waits, the second
  // falls back — see the effect below.
  const resolved = resourceItem(resource, discovered)
  const item = resolved ?? DEFAULT_ITEM
  const namespaced = item.scope === 'namespaced'

  useEffect(() => {
    if (clusterId === null && reachable.length > 0) {
      setClusterId(reachable[0].id)
    }
  }, [clusterId, reachable])

  // Namespaces reload whenever the cluster changes; the current namespace is
  // dropped so it cannot leak across clusters.
  useEffect(() => {
    if (!cluster) return

    let cancelled = false
    setNamespace('')
    setNamespaces([])
    setError(null)

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
        if (!cancelled) setError(errorMessage(err, 'Could not list namespaces.'))
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

  const load = useCallback(async () => {
    if (!cluster) return
    if (namespaced && !namespace) return
    // Nothing to read yet: the selection is a discovered kind and discovery has
    // not come back. Reading the fallback here would show Pods under another
    // resource's heading for as long as that takes.
    if (!resolved) return

    setLoading(true)
    try {
      setLoaded(await loadResource(item, cluster.id, namespace, namespaces))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, `Could not read ${item.label.toLowerCase()} from this cluster.`))
      setLoaded(null)
    } finally {
      setLoading(false)
    }
  }, [cluster, namespace, namespaced, namespaces, item, resolved])

  useEffect(() => {
    void load()
  }, [load])

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

  const unavailable = (loaded?.kind === 'routes' || loaded?.kind === 'custom') && !loaded.available
  const count = loaded?.rows.length ?? 0
  const allNamespaces = namespaced && namespace === ALL_NAMESPACES

  return (
    <AppShell
      title="Explore"
      sidebar={
        <ExploreSidebar categories={categories} selected={resource} onSelect={setResource} />
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
          <div className="w-44">
            <Select
              aria-label="Cluster"
              size="sm"
              value={clusterId ?? ''}
              onChange={(event) => setClusterId(Number(event.target.value))}
            >
              {reachable.map((entry) => (
                <option key={entry.id} value={entry.id}>
                  {entry.name}
                </option>
              ))}
            </Select>
          </div>

          {cluster ? <EnvironmentTag environment={cluster.environment} /> : null}

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

          {loading && !loaded ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">Reading the cluster…</p>
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
