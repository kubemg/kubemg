import type { Cluster, VulnClusterCounts, VulnLevel, VulnWorkloadSummary } from '../api/types'
import type { ResourceKey } from './resources'
import type { Tone } from './status'

/*
 * Vulnerability findings, as the console draws them. The server decides who
 * sees them and how urgent each workload is; everything here is presentation
 * over that answer — which tone a level gets, how a row finds its summary, and
 * what the sidebar counts.
 */

/**
 * Whether to ask at all. The server is the authority — it answers 404 to
 * anyone outside the audience — so this only spares a request it would refuse:
 * a cluster-admin grant with no namespace scope, which is also what an
 * administrator's cluster list carries.
 */
export function mayAskForVulns(cluster: Pick<Cluster, 'k8s_role' | 'namespaces' | 'connection_mode'>): boolean {
  return (
    cluster.connection_mode === 'agent' &&
    cluster.k8s_role === 'cluster-admin' &&
    (cluster.namespaces ?? []).length === 0
  )
}

/** The drawer kinds a scanner report belongs to. A controlled pod's findings are its workload's. */
const PANEL_KINDS: ReadonlySet<string> = new Set(['deployments', 'statefulsets', 'daemonsets', 'cronjobs', 'jobs'])

/** Whether a drawer draws the Vulnerabilities panel at all. */
export function showsVulnerabilities(
  cluster: Pick<Cluster, 'k8s_role' | 'namespaces' | 'connection_mode'>,
  kind: string,
  namespace?: string,
): boolean {
  return Boolean(namespace) && PANEL_KINDS.has(kind) && mayAskForVulns(cluster)
}

/** Rust only for danger, amber only for warn; a `none` row carries no colour. */
export function vulnTone(level: VulnLevel): Tone | null {
  switch (level) {
    case 'danger':
      return 'bad'
    case 'warn':
      return 'warn'
    default:
      return null
  }
}

/** How a list row and a summary meet: Kind, namespace, name. */
export function vulnKey(kind: string, namespace: string, name: string): string {
  return `${kind}/${namespace}/${name}`
}

export function indexVulnSummaries(rows: readonly VulnWorkloadSummary[]): Map<string, VulnWorkloadSummary> {
  const index = new Map<string, VulnWorkloadSummary>()
  for (const row of rows) index.set(vulnKey(row.kind, row.namespace, row.name), row)
  return index
}

/** The Explore resource keys a summary row can belong to, and their Kinds. */
export const VULN_RESOURCE_KINDS: Readonly<Record<string, string>> = {
  deployments: 'Deployment',
  statefulsets: 'StatefulSet',
  daemonsets: 'DaemonSet',
  cronjobs: 'CronJob',
  jobs: 'Job',
  pods: 'Pod',
  replicasets: 'ReplicaSet',
}

/** The Explore resource key for a summary's Kind, so a row opens its drawer. */
export function vulnResourceKey(kind: string): ResourceKey | null {
  for (const [key, value] of Object.entries(VULN_RESOURCE_KINDS)) {
    if (value === kind) return key as ResourceKey
  }
  return null
}

/** The words on a row's badge — what makes it coloured, never just a count. */
export function vulnBadgeText(row: VulnWorkloadSummary): string {
  if (row.kev > 0) {
    return `${row.kev} known exploited`
  }
  if (row.level === 'danger') {
    return `${row.fixable_critical} fixable critical, exposed`
  }
  const fixable = row.fixable_critical + row.fixable_high
  return `${fixable} fixable ${row.fixable_critical > 0 ? 'critical/high' : 'high'}`
}

/** The badge's hover: the whole count, so the colour is never the only signal. */
export function vulnBadgeTitle(row: VulnWorkloadSummary): string {
  const parts = [
    `${row.critical} critical`,
    `${row.high} high`,
    `${row.medium} medium`,
    `${row.low} low`,
  ]
  const fixable = row.fixable_critical + row.fixable_high
  const extra = [
    row.kev > 0 ? `${row.kev_ids.join(', ')} on CISA's known-exploited catalogue` : '',
    row.exposed ? 'reachable from outside the cluster' : '',
    row.accepted > 0 ? `${row.accepted} accepted, not counted` : '',
  ].filter(Boolean)
  return [`${parts.join(', ')}`, `${fixable} critical/high with a fix`, ...extra].join(' - ')
}

/** EPSS as a person reads it: the 30-day probability, and where it ranks. */
export function epssText(score: number, percentile?: number): string {
  const percent = score * 100
  const shown = percent >= 10 ? percent.toFixed(0) : percent.toFixed(1)
  const rank = percentile !== undefined ? `, top ${Math.max(0.1, (1 - percentile) * 100).toFixed(percentile > 0.99 ? 1 : 0)}%` : ''
  return `EPSS ${shown}%${rank}`
}

/** The hover on a known-exploited marker. */
export function kevText(kev: { added?: string; due?: string; ransomware: boolean; name?: string }): string {
  const parts = ["On CISA's Known Exploited Vulnerabilities catalogue"]
  if (kev.added) parts.push(`added ${kev.added.slice(0, 10)}`)
  if (kev.due) parts.push(`remediation due ${kev.due.slice(0, 10)}`)
  if (kev.ransomware) parts.push('known to be used in ransomware campaigns')
  return parts.join(' - ')
}

/** How long an accepted risk holds. The server refuses anything past a year. */
export const ACCEPT_EXPIRY_CHOICES: ReadonlyArray<{ days: number; label: string }> = [
  { days: 30, label: 'In 30 days' },
  { days: 90, label: 'In 90 days' },
  { days: 180, label: 'In 180 days' },
  { days: 365, label: 'In a year' },
  { days: 0, label: 'Never - until withdrawn' },
]

/** The expiry sent for a choice; 0 sends none. */
export function acceptExpiry(days: number, now: Date): string | undefined {
  if (days <= 0) return undefined
  return new Date(now.getTime() + days * 86_400_000).toISOString()
}

/** The sidebar's number: workloads that need a look, danger or warn. */
export function vulnNavCount(counts: VulnClusterCounts | undefined): number | null {
  if (!counts) return null
  const total = counts.danger + counts.warn
  return total > 0 ? total : null
}

/** Whether the rail marks the cluster — rust only, so the dot means something. */
export function vulnRailDot(counts: VulnClusterCounts | undefined): boolean {
  return (counts?.danger ?? 0) > 0
}

export type VulnLevelFilter = 'attention' | 'all'

/** The page's rows: by default only what has a colour. */
export function filterVulnRows(
  rows: readonly VulnWorkloadSummary[],
  filter: VulnLevelFilter,
  search: string,
): VulnWorkloadSummary[] {
  const needle = search.trim().toLowerCase()
  return rows.filter((row) => {
    if (filter === 'attention' && row.level === 'none') return false
    if (!needle) return true
    return (
      row.name.toLowerCase().includes(needle) ||
      row.namespace.toLowerCase().includes(needle) ||
      row.images.some((image) => image.toLowerCase().includes(needle))
    )
  })
}

/** Severity words for a finding, in the order the drawer sorts them. */
export function severityTone(severity: string): Tone {
  switch (severity.toUpperCase()) {
    case 'CRITICAL':
      return 'bad'
    case 'HIGH':
      return 'warn'
    default:
      return 'idle'
  }
}

/**
 * "Who could have touched it since it was known": the cluster's audit trail
 * for this workload's namespace, matching its name, from the day the CVE was
 * published. `from` is written as the trail's own date-time box writes it, in
 * local time. Null for a cluster-wide row or a finding with no date.
 */
export function disclosureAuditLink(
  clusterId: number,
  namespace: string,
  name: string,
  published: string | undefined,
): string | null {
  if (!namespace || !published) return null
  const at = new Date(published)
  if (Number.isNaN(at.getTime())) return null
  const pad = (value: number) => String(value).padStart(2, '0')
  const from = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`
  const query = new URLSearchParams({ namespace, q: name, from })
  return `/clusters/${clusterId}/audit?${query.toString()}`
}

/** The summary row Kubernetes' own components are counted on. */
export function isKubernetesRow(row: Pick<VulnWorkloadSummary, 'kind' | 'namespace'>): boolean {
  return row.kind === 'Kubernetes' && row.namespace === ''
}
