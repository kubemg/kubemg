import { fetchClusterConsoles } from '../api/client'
import type { ClusterConsole, ConsoleKind, DatasourceProvider } from '../api/types'
import { useCachedQuery } from './query'

/**
 * The other consoles a cluster is operated from.
 *
 * A link, never an embed and never a proxy: an iframe would inherit this
 * console's origin and its session, and proxying a whole Grafana down the agent
 * tunnel would mean carrying a second application's routing, assets and
 * websockets inside a transport built for the Kubernetes API. KubeMG stores an
 * address, holds no session for either tool, and the operator signs in to them
 * as themselves — so a link is safe to show as widely as the cluster is.
 */
export interface ConsoleInfo {
  label: string
  /** What it answers that KubeMG does not, in one line. */
  purpose: string
  /** The address an operator should recognise, as a placeholder. */
  placeholder: string
  /** What the optional identifier is *for*, or null where the kind has none. */
  refLabel: string | null
  refHint?: string
}

export const CONSOLES: Record<ConsoleKind, ConsoleInfo> = {
  grafana: {
    label: 'Grafana',
    purpose:
      'The dashboards behind the charts here. KubeMG draws a fixed catalogue; the moment a question outgrows it, this is where it is answered.',
    placeholder: 'https://grafana.example.com',
    // Grafana's identifier is per *datasource* rather than per console — one
    // Grafana holds the metrics datasource and the logs one — so it is asked for
    // on the datasource form instead.
    refLabel: null,
  },
  argocd: {
    label: 'Argo CD',
    purpose:
      'What deployed half of what Explore lists. A workload that keeps reverting was reverted by something, and this is where that is visible.',
    placeholder: 'https://argocd.example.com',
    refLabel: 'Project',
    refHint: 'Optional. Only used to label the link — the application view is found by name.',
  },
}

export const CONSOLE_KINDS: ConsoleKind[] = ['grafana', 'argocd']

/**
 * The label Argo CD writes on everything it owns. A workload carrying it was
 * deployed by an Argo application of that name, which is the one thing needed to
 * link straight to it.
 */
export const ARGO_INSTANCE_LABEL = 'argocd.argoproj.io/instance'

/**
 * argoApplicationHref points at one Argo CD application. Argo's application view
 * is a path — which is why this one is built here rather than on the server, as
 * the Grafana Explore link has to be: that one carries a *query*, and in KubeMG a
 * query is always the server's.
 *
 * The name comes off a label Argo wrote, so it is encoded into its segment
 * rather than trusted to be one.
 */
export function argoApplicationHref(base: string, name: string): string {
  const trimmedBase = base.replace(/\/+$/, '')
  const trimmedName = name.trim()
  if (!trimmedBase || !trimmedName) return ''
  return `${trimmedBase}/applications/${encodeURIComponent(trimmedName)}`
}

/** What the datasource's own UI is called, for the link that opens it. */
export function datasourceUILabel(provider: DatasourceProvider): string {
  switch (provider) {
    case 'victoriametrics':
    case 'victorialogs':
      return 'vmui'
    case 'prometheus':
      return 'Prometheus UI'
    case 'thanos':
      return 'Thanos UI'
    default:
      return 'Query UI'
  }
}

/**
 * useClusterConsole answers "does this cluster have an X, and where" for a
 * surface that is about something else — a resource drawer, a workload row. It
 * goes through the shared short-TTL cache because a console address is the same
 * answer for every object in a cluster, and re-reading it per drawer open would
 * be a request per click for a value that does not move.
 */
export function useClusterConsole(
  clusterId: number | null,
  kind: ConsoleKind,
): ClusterConsole | null {
  const { data } = useCachedQuery(
    clusterId === null ? null : `consoles:${clusterId}`,
    () => fetchClusterConsoles(clusterId as number),
  )
  return data?.consoles.find((console) => console.kind === kind) ?? null
}
