import type { Cluster } from '../api/types'

/**
 * Where picking a cluster goes.
 *
 * Picking a cluster is asking to look inside it, so a cluster with a live agent
 * tunnel opens in Explore — the fleet list and the palette are the cluster
 * switcher, which is why Explore has no picker of its own. A cluster with no
 * tunnel has no live state to show, so it opens on its own page, which is where
 * its connection and access are managed either way.
 */
export function clusterHref(cluster: Cluster): string {
  return cluster.connection_mode === 'agent' && cluster.agent_attached
    ? `/explore/${cluster.id}`
    : `/clusters/${cluster.id}`
}

/** Whether a path is looking at this cluster, in either of the two places it can be. */
export function isClusterPath(pathname: string, id: number): boolean {
  return pathname === `/clusters/${id}` || pathname === `/explore/${id}`
}
