import type { Cluster } from '../api/types'
import { isBehind, newestAgentVersion } from './fleet'
import { TIME_RANGE_PARAM } from './timerange'
import type { TimeRangeId } from './timerange'

/*
 * The fleet's first row: what needs a decision today, each figure a link onto
 * the thing it counts.
 *
 * It replaced six readings — clusters, reachable, unreachable, tunnels open,
 * nodes, environments — that were correct and inert. The first three restated
 * one another, a tunnel belongs to its own cluster's row, and the number of
 * environments is not an operational fact at all. What an operator acts on is
 * somebody waiting on an approval, calls being refused, credentials about to
 * stop working and agents that need re-applying.
 *
 * Three rules, each pinned in the test beside this file:
 *
 *   - **A count is three states, not one.** `undefined` has not been read yet,
 *     `null` could not be read, and only a number is a reading. Zero and
 *     "could not read" must never look the same — a strip that turns a failed
 *     read into 0 says "all clear" at exactly the moment it knows nothing.
 *   - **A figure and its link ask one question.** The refusals figure is counted
 *     with `failed=true` over `STRIP_WINDOW` and opens the trail on exactly that,
 *     so the rows behind the number are the number.
 *   - **Drift is measured against the fleet.** `agentsBehind` uses the same
 *     `newestAgentVersion` the queue and the table do, never a version this
 *     build carries.
 */

/** The window both time-bound figures read over, in the console's own vocabulary. */
export const STRIP_WINDOW: TimeRangeId = '24h'

/** Not read yet (`undefined`), could not be read (`null`), or a reading. */
export type Count = number | null | undefined

export type StripCounts = {
  /** Access requests waiting — the caller's own for a non-admin, which the server narrows. */
  pending: Count
  /** Refused or failed calls over STRIP_WINDOW. Read for an administrator only. */
  refused: Count
  /** Live kubeconfigs running out within STRIP_WINDOW — the caller's own for a non-admin. */
  expiring: Count
}

export type StripFigure = {
  key: 'requests' | 'refused' | 'expiring' | 'behind'
  value: Count
  label: string
  to: string
  /** The tone a non-zero reading takes. Zero is always neutral. */
  tone: 'bad' | 'warn'
}

/** The query parameter the clusters page narrows on, and the value it narrows to. */
export const AGENT_FILTER_PARAM = 'agent'
export const AGENT_BEHIND = 'behind'

/**
 * The clusters whose agent is behind the newest one running anywhere in the
 * fleet. Shared by the figure and the clusters page it opens, so the count and
 * the rows cannot disagree.
 */
export function agentsBehind(clusters: Cluster[]): Cluster[] {
  const newest = newestAgentVersion(clusters)
  if (!newest) return []
  return clusters.filter((cluster) =>
    Boolean(cluster.agent_version && isBehind(cluster.agent_version, newest)),
  )
}

export function refusedHref(): string {
  return `/admin/audit?${TIME_RANGE_PARAM}=${STRIP_WINDOW}&failed=true`
}

export function operatorFigures(counts: StripCounts, clusters: Cluster[]): StripFigure[] {
  return [
    {
      key: 'requests',
      value: counts.pending,
      label: 'requests waiting',
      to: '/admin/access-requests',
      tone: 'warn',
    },
    {
      key: 'refused',
      value: counts.refused,
      label: `refused · ${STRIP_WINDOW}`,
      to: refusedHref(),
      tone: 'bad',
    },
    {
      key: 'expiring',
      value: counts.expiring,
      label: `kubeconfigs expiring · ${STRIP_WINDOW}`,
      to: `/admin/credentials?expiring=${STRIP_WINDOW}`,
      tone: 'warn',
    },
    {
      // Derived from the list the page already holds: always a reading.
      key: 'behind',
      value: agentsBehind(clusters).length,
      label: 'agents behind',
      to: `/admin/clusters?${AGENT_FILTER_PARAM}=${AGENT_BEHIND}`,
      tone: 'warn',
    },
  ]
}

/*
 * A developer is shown only what they can act on, decided per figure:
 *
 *   - their own requests waiting — theirs to withdraw or to chase;
 *   - their own kubeconfigs about to run out — theirs to replace before a job
 *     that uses one breaks.
 *
 * Not refusals: the trail would narrow them to their own calls, and a grant too
 * small for the work is fixed by asking for more, which the page already
 * offers. Not agent drift: only whoever installed the agent can re-apply it.
 * Neither figure is an admin-only read; both endpoints narrow a non-admin to
 * their own rows on the server.
 */
export function developerFigures(counts: StripCounts): StripFigure[] {
  return [
    {
      key: 'requests',
      value: counts.pending,
      label: 'your requests waiting',
      to: '/me/access',
      tone: 'warn',
    },
    {
      key: 'expiring',
      value: counts.expiring,
      label: `your kubeconfigs expiring · ${STRIP_WINDOW}`,
      to: `/me/credentials?expiring=${STRIP_WINDOW}`,
      tone: 'warn',
    },
  ]
}
