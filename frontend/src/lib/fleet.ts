import type { Cluster } from '../api/types'
import { clusterPageHref } from './navigation'
import { relativeAge } from './time'

/*
 * What is waiting on an administrator, derived from the fleet the page already
 * holds.
 *
 * It lives here rather than beside the component that draws it for the reason
 * `lib/insights.ts` does: this is a derivation over clusters, it is the whole
 * substance of the queue, and a derivation is the part worth pinning in a test
 * rather than the markup around it.
 */

export type QueueItem = {
  key: string
  tone: 'bad' | 'warn'
  subject: string
  detail: string
  action: string
  to: string
}

/**
 * Compares two agent versions the way an operator reads them, and answers
 * `null` for anything it cannot parse rather than guessing. A build tagged
 * something this does not understand must not be reported as behind — a false
 * "upgrade me" in the queue costs the queue its credibility, which is the only
 * thing it has.
 */
function versionOrder(version: string): number[] | null {
  const parts = version.replace(/^v/, '').split('.')
  if (parts.length < 2) return null
  const numbers = parts.map((part) => Number(/^\d+/.exec(part)?.[0]))
  return numbers.some((value) => Number.isNaN(value)) ? null : numbers
}

export function isBehind(version: string, newest: string): boolean {
  const a = versionOrder(version)
  const b = versionOrder(newest)
  if (!a || !b) return false
  for (let index = 0; index < Math.max(a.length, b.length); index += 1) {
    const left = a[index] ?? 0
    const right = b[index] ?? 0
    if (left !== right) return left < right
  }
  return false
}

/**
 * The newest agent version running anywhere in this fleet, which is what drift
 * is measured against.
 *
 * Deliberately not a number this build carries: the bastion does not publish
 * the agent version it expects, and hard-coding one here would have every
 * install start lying the day an agent ships without the console. "One of your
 * own clusters already runs a newer one" is a fact that cannot go stale and
 * needs no new API. Shared by the queue and the table so the two cannot
 * disagree about which cluster is behind.
 */
export function newestAgentVersion(clusters: Cluster[]): string | null {
  return clusters
    .map((cluster) => cluster.agent_version)
    .filter((version): version is string => Boolean(version))
    .reduce<string | null>(
      (best, version) => (best === null || isBehind(best, version) ? version : best),
      null,
    )
}

/**
 * What is waiting on an administrator, derived entirely from state the page
 * already holds. Ordered by what stops work soonest: a cluster that cannot be
 * reached, one that never arrived, somebody blocked on an approval, then an
 * upgrade that can wait until Thursday.
 */
export function fleetQueue(clusters: Cluster[], pendingRequests: number): QueueItem[] {
  const items: QueueItem[] = []

  for (const cluster of clusters) {
    if (cluster.status !== 'unhealthy') continue
    items.push({
      key: `down-${cluster.id}`,
      tone: 'bad',
      subject: cluster.name,
      detail:
        cluster.status_message ??
        (cluster.connection_mode === 'agent'
          ? 'the agent has stopped dialling in'
          : 'the API server could not be reached'),
      action: 'Open cluster',
      to: clusterPageHref(cluster.id, 'dashboard'),
    })
  }

  for (const cluster of clusters) {
    // Registered, agent mode, and has never once attached: the install command
    // is the thing that has not happened, which is a different fix from a
    // tunnel that closed.
    if (cluster.connection_mode !== 'agent') continue
    if (cluster.status !== 'pending' || cluster.agent_connected_at) continue
    items.push({
      key: `idle-${cluster.id}`,
      tone: 'bad',
      subject: cluster.name,
      detail: `registered ${relativeAge(cluster.created_at)}, never dialled in — the install command may not have run`,
      action: 'Install command',
      to: clusterPageHref(cluster.id, 'dashboard'),
    })
  }

  if (pendingRequests > 0) {
    items.push({
      key: 'jit',
      tone: 'warn',
      subject: `${pendingRequests} access ${pendingRequests === 1 ? 'request' : 'requests'}`,
      detail: 'waiting on an approval',
      action: 'Review',
      to: '/admin/access-requests',
    })
  }

  const newest = newestAgentVersion(clusters)
  if (newest) {
    for (const cluster of clusters) {
      if (!cluster.agent_version || !isBehind(cluster.agent_version, newest)) continue
      items.push({
        key: `drift-${cluster.id}`,
        tone: 'warn',
        subject: cluster.name,
        detail: `agent ${cluster.agent_version} is behind ${newest} running elsewhere in the fleet — re-apply its manifests`,
        action: 'Manifests',
        to: clusterPageHref(cluster.id, 'dashboard'),
      })
    }
  }

  return items
}
