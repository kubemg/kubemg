import type { Cluster } from '../api/types'

/**
 * Where picking a cluster goes.
 *
 * A cluster now has an address space of its own (`/clusters/:id/*`), so
 * picking one is choosing a view inside that space rather than choosing
 * between two unrelated pages. Picking a cluster is asking to look inside it,
 * so a cluster with a live agent tunnel opens on its resources — the fleet
 * list, the palette and the entity switcher are the cluster switcher, which is
 * why Explore has no picker of its own. A cluster with no tunnel has no live
 * state to show, so it opens on its summary, which is where its connection and
 * access are managed either way.
 */
export function clusterHref(cluster: Cluster): string {
  return cluster.connection_mode === 'agent' && cluster.agent_attached
    ? `/clusters/${cluster.id}/explore`
    : `/clusters/${cluster.id}/summary`
}

/** Whether a path is looking at this cluster, anywhere in its address space. */
export function isClusterPath(pathname: string, id: number): boolean {
  return pathname === `/clusters/${id}` || pathname.startsWith(`/clusters/${id}/`)
}

/** The views a cluster's own address space holds. */
export type ClusterView = 'summary' | 'explore' | 'audit' | 'events' | 'capacity' | 'security'

/** Which of a cluster's own views the address currently names, Summary when it
    does not — the same view `clusterHref` opens by default. */
export function currentClusterView(pathname: string, clusterId: number): ClusterView {
  const match = pathname.match(
    new RegExp(`^/clusters/${clusterId}/(summary|explore|audit|events|capacity|security)(?:/|$)`),
  )
  return (match?.[1] as ClusterView | undefined) ?? 'summary'
}

/**
 * Every cluster has a summary and a trail. The rest are read *from the
 * cluster* — its resources, the events it recorded, its allocation, its
 * posture — and are agent-only for the same reason, so switching from one of
 * them to a cluster with no tunnel lands on that cluster's summary rather than
 * on a page that can only refuse.
 */
const LIVE_VIEWS: readonly ClusterView[] = ['explore', 'events', 'capacity', 'security']

export function hasClusterView(cluster: Cluster, view: ClusterView): boolean {
  if (!LIVE_VIEWS.includes(view)) return true
  return cluster.connection_mode === 'agent' && cluster.agent_attached
}

/**
 * Where switching to `target` goes while `view` is open. Switching clusters
 * keeps whichever view you were reading — Summary stays Summary, Explore stays
 * Explore — and falls back to Summary on a target that cannot serve it. Both
 * switchers (the header's and the panel's) go through this, so a click in one
 * cannot land somewhere a click in the other would not.
 */
export function clusterViewHref(target: Cluster, view: ClusterView): string {
  return `/clusters/${target.id}/${hasClusterView(target, view) ? view : 'summary'}`
}
