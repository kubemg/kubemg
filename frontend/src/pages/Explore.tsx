import { useCallback, useEffect, useState } from 'react'
import { Boxes, RefreshCw } from 'lucide-react'
import {
  errorMessage,
  fetchCRDs,
  fetchConfigMaps,
  fetchCronJobs,
  fetchDaemonSets,
  fetchDeployments,
  fetchHTTPRoutes,
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
import type { Namespace, Pod } from '../api/types'
import { AppShell } from '../components/AppShell'
import { ExploreSidebar } from '../components/ExploreSidebar'
import { PodDrawer } from '../components/PodDrawer'
import { ResourceView } from '../components/ResourceTables'
import type { LoadedResource } from '../components/ResourceTables'
import { YamlDrawer } from '../components/YamlDrawer'
import type { ManifestTarget } from '../components/YamlDrawer'
import {
  Button,
  EmptyState,
  EnvironmentTag,
  Notice,
  Pill,
  Select,
} from '../components/primitives'
import type { ResourceKey } from '../lib/resources'
import {
  ALL_NAMESPACES,
  RESOURCE_CATEGORIES,
  resourceItem,
  resourceSingular,
} from '../lib/resources'
import { useClusters } from '../state/clusters-context'

/**
 * loadResource reads one list. The tagged result is what keeps the table
 * rendering honest: the loader decides the shape, and the compiler holds the
 * table to it.
 */
async function loadResource(
  resource: ResourceKey,
  clusterId: number,
  namespace: string,
  namespaces: Namespace[],
): Promise<LoadedResource> {
  switch (resource) {
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
  const [selected, setSelected] = useState<Pod | null>(null)
  const [manifest, setManifest] = useState<ManifestTarget | null>(null)

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Only agent clusters have a tunnel to read through; a direct-mode cluster has
  // no live state to show.
  const reachable = clusters.filter(
    (cluster) => cluster.connection_mode === 'agent' && cluster.agent_attached,
  )
  const cluster = reachable.find((entry) => entry.id === clusterId) ?? null

  const item = resourceItem(resource)
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
        if (result.namespaces.length > 0) setNamespace(result.namespaces[0].name)
      })
      .catch((err) => {
        if (!cancelled) setError(errorMessage(err, 'Could not list namespaces.'))
      })

    return () => {
      cancelled = true
    }
  }, [cluster])

  const load = useCallback(async () => {
    if (!cluster) return
    if (namespaced && !namespace) return

    setLoading(true)
    try {
      setLoaded(await loadResource(resource, cluster.id, namespace, namespaces))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, `Could not read ${item.label.toLowerCase()} from this cluster.`))
      setLoaded(null)
    } finally {
      setLoading(false)
    }
  }, [cluster, namespace, namespaced, resource, namespaces, item.label])

  useEffect(() => {
    void load()
  }, [load])

  // A drawer belongs to the list it was opened from; switching resources closes
  // it rather than leaving it open over a list it does not come from.
  useEffect(() => {
    setSelected(null)
    setManifest(null)
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

  const unavailable = loaded?.kind === 'routes' && !loaded.available
  const count = loaded?.rows.length ?? 0
  const allNamespaces = namespaced && namespace === ALL_NAMESPACES

  return (
    <AppShell
      title="Explore"
      sidebar={<ExploreSidebar selected={resource} onSelect={setResource} />}
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
                onChange={(event) => setNamespace(event.target.value)}
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
              {RESOURCE_CATEGORIES.map((category) => (
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
              onSelectPod={setSelected}
              // The page is the only place that knows which resource it asked
              // for: several kinds share a table, and a manifest has to be
              // addressed by what the object actually is.
              onManifest={(name, rowNamespace, editing) =>
                setManifest({
                  kind: resource,
                  label: resourceSingular(item),
                  name,
                  namespace: namespaced ? (rowNamespace ?? namespace) : undefined,
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

      {selected && cluster ? (
        <PodDrawer
          cluster={cluster}
          pod={selected}
          onClose={() => setSelected(null)}
          onRefresh={load}
        />
      ) : null}

      {manifest && cluster ? (
        <YamlDrawer
          cluster={cluster}
          target={manifest}
          onClose={() => setManifest(null)}
          onApplied={load}
        />
      ) : null}
    </AppShell>
  )
}
