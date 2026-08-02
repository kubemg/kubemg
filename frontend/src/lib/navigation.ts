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
