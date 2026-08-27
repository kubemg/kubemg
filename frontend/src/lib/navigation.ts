import type { Cluster } from '../api/types'
import { ALL_NAMESPACES } from './resources'

/**
 * The console has two address spaces, and they answer to two different people.
 *
 * `/clusters/:id/*` is a cluster: its dashboard, the reads taken from it, and
 * every kind of object in it. This is the space a developer or an analyst lives
 * in, and it is the console's default — picking a cluster is asking to look
 * inside one, so it opens on its resources rather than on a page about it.
 *
 * `/admin/*` is the fleet's administration: registering clusters, mapping
 * identity, guardrails, settings, and the fleet-wide trail. Those are the
 * platform team's work — done rarely, change-controlled — so they sit behind
 * one door rather than one row above the thing somebody opens forty times a
 * day.
 *
 * `/me/*` is the operator's own business: the access they hold and the access
 * they are waiting on.
 */

/** Whether a cluster has a live tunnel, which is what any read *from* it needs. */
export function hasTunnel(cluster: Cluster): boolean {
  return cluster.connection_mode === 'agent' && cluster.agent_attached
}

/**
 * A page a cluster carries that is not one of its resource lists. Everything
 * else under `/clusters/:id/` is a resource key, which is why these five names
 * are reserved: the resource route is a splat, so a kind called `capacity`
 * would otherwise be unreachable.
 */
export type ClusterPage = 'dashboard' | 'events' | 'capacity' | 'security' | 'audit'

export const CLUSTER_PAGES: readonly ClusterPage[] = [
  'dashboard',
  'events',
  'capacity',
  'security',
  'audit',
]

/**
 * The pages that read the cluster itself. Dashboard and the audit trail are
 * served from KubeMG's own records — the connection it holds, the calls it made
 * — so they survive an agent that has stopped dialling in. The other three are
 * reads through the tunnel and cannot.
 */
const LIVE_PAGES: readonly ClusterPage[] = ['events', 'capacity', 'security']

export function pageNeedsTunnel(page: ClusterPage): boolean {
  return LIVE_PAGES.includes(page)
}

function isClusterPage(segment: string): segment is ClusterPage {
  return (CLUSTER_PAGES as readonly string[]).includes(segment)
}

/**
 * What a `/clusters/:id/...` address is looking at: one of the cluster's own
 * pages, or one of its resource lists. A resource key can carry slashes of its
 * own (`crd:group/version/plural`), so it is the whole tail rather than one
 * segment.
 */
export type ClusterSlot =
  | { kind: 'page'; page: ClusterPage }
  | { kind: 'resource'; key: string }

/** What every cluster opens on when it has nothing to read through. */
export const DEFAULT_PAGE: ClusterPage = 'dashboard'

/** What a cluster with a tunnel opens on: the list people actually arrive for. */
export const DEFAULT_RESOURCE = 'pods'

export function clusterPageHref(clusterId: number, page: ClusterPage): string {
  return `/clusters/${clusterId}/${page}`
}

/**
 * A resource list's address, carrying a query string when one applies — the
 * namespace scope travels with the kind, so a click in the tree keeps the
 * namespace the operator is working in.
 */
export function resourceHref(clusterId: number, key: string, search = ''): string {
  const qs = search.replace(/^\?/, '')
  return `/clusters/${clusterId}/${key}${qs ? `?${qs}` : ''}`
}

/**
 * Where picking a cluster goes. A cluster with a tunnel opens on its
 * resources — that is what the fleet list, the rail, the switchers and the
 * palette are all asking for. One without has no live state to show, so it
 * opens on its dashboard, which is where its connection is explained.
 */
export function clusterHref(cluster: Cluster): string {
  return hasTunnel(cluster)
    ? resourceHref(cluster.id, DEFAULT_RESOURCE)
    : clusterPageHref(cluster.id, DEFAULT_PAGE)
}

/** Whether a path is looking at this cluster, anywhere in its address space. */
export function isClusterPath(pathname: string, id: number): boolean {
  return pathname === `/clusters/${id}` || pathname.startsWith(`/clusters/${id}/`)
}

/** The cluster id a path names, or `null` outside a cluster's space entirely. */
export function clusterIdFromPath(pathname: string): number | null {
  const match = /^\/clusters\/(\d+)(?:\/|$)/.exec(pathname)
  return match ? Number(match[1]) : null
}

/** Which slot of `clusterId`'s space the address names; its dashboard by default. */
export function currentClusterSlot(pathname: string, clusterId: number): ClusterSlot {
  const prefix = `/clusters/${clusterId}/`
  if (!pathname.startsWith(prefix)) return { kind: 'page', page: DEFAULT_PAGE }
  const tail = pathname.slice(prefix.length)
  if (!tail) return { kind: 'page', page: DEFAULT_PAGE }
  const head = tail.split('/')[0]
  return isClusterPage(head) ? { kind: 'page', page: head } : { kind: 'resource', key: tail }
}

/**
 * Where switching to `target` goes while `slot` is open. Switching clusters
 * keeps what you were reading — Pods stays Pods, Capacity stays Capacity — and
 * falls back to the dashboard on a cluster that cannot serve it. Every switcher
 * goes through this, so a click in one cannot land somewhere a click in another
 * would not.
 */
export function clusterSlotHref(target: Cluster, slot: ClusterSlot, search = ''): string {
  if (slot.kind === 'resource') {
    return hasTunnel(target)
      ? resourceHref(target.id, slot.key, search)
      : clusterPageHref(target.id, DEFAULT_PAGE)
  }
  return pageNeedsTunnel(slot.page) && !hasTunnel(target)
    ? clusterPageHref(target.id, DEFAULT_PAGE)
    : clusterPageHref(target.id, slot.page)
}

/* ── Administration ─────────────────────────────────────────────────────── */

/** The door's own landing: the inventory, which is what it is entered for. */
export const ADMIN_HOME = '/admin/clusters'

export function isAdminPath(pathname: string): boolean {
  return pathname === '/admin' || pathname.startsWith('/admin/')
}

/** The operator's own access — theirs to read whatever their role is. */
export const ACCESS_HOME = '/me/access'

/**
 * The operator's own credentials: the kubeconfigs they hold, and the password
 * that opens their sessions. Not admin-only for the same reason ACCESS_HOME is
 * not — revoking a file you know you lost, or rotating a password you think has
 * leaked, must not require finding an administrator first.
 */
export const CREDENTIALS_HOME = '/me/credentials'

/**
 * A link into one cluster's events timeline, narrowed to one object.
 *
 * It lives here rather than beside either caller because two surfaces need the
 * same link for the same reason: a list header and a dashboard both name objects
 * that are in trouble, and "why" is answered by what the cluster recorded
 * against that object rather than by the object itself.
 */
export function eventsHref(
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
  return `${clusterPageHref(clusterId, 'events')}?${params.toString()}`
}
