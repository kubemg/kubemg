import type { PostureFinding } from '../api/types'

/**
 * Reading a posture scan the way a security team works through one.
 *
 * The scan already answers the hard question: every finding carries `permits`,
 * the server's rank of what a firing *permits* a workload to do, and the list
 * arrives sorted by it. What the page did with that was nothing — 36 findings at
 * identical visual weight, no grouping, no filter, no way to hand the result to
 * the team that owns the fix. A list you can read but cannot work through.
 *
 * Everything here is derived from `permits` rather than invented alongside it.
 * A second severity scale defined in the browser would be free to disagree with
 * the ranking the server asserts as a property of each rule, and then the page's
 * order and its stripes would tell two different stories about the same finding.
 */

/** The four bands a reader sorts into. They are names for ranges of `permits`,
    not a new opinion about severity. */
export type PostureSeverity = 'critical' | 'high' | 'medium' | 'low'

export const POSTURE_SEVERITIES: readonly PostureSeverity[] = ['critical', 'high', 'medium', 'low']

/**
 * Which band a finding falls in.
 *
 * The cuts are placed at the gaps the server's own ranking already has, and
 * each one is a real change in kind rather than a round number:
 *
 *   - **critical** (≥90) — privileged, and host namespaces. These own the node
 *     or everything else on it.
 *   - **high** (≥80) — hostPath. Arbitrary node filesystem, bounded by the mount
 *     rather than by policy.
 *   - **medium** (≥45) — no NetworkPolicy, and an automounted default
 *     ServiceAccount token. Both widen what a compromise reaches; neither is one.
 *   - **low** (below) — no non-root declaration (genuinely uncertain: the image
 *     may already run non-root, the manifest just does not say so) and no
 *     resource limits (a noisy neighbour, not an escape).
 *
 * An unknown rank — a rule added on the server before this file learns about it
 * — lands in a band by the same arithmetic rather than in a special case, which
 * is the point of deriving from the number instead of switching on the rule.
 */
export function severityOf(finding: Pick<PostureFinding, 'permits'>): PostureSeverity {
  if (finding.permits >= 90) return 'critical'
  if (finding.permits >= 80) return 'high'
  if (finding.permits >= 45) return 'medium'
  return 'low'
}

/**
 * How many of each band, and how many of those are still unacknowledged.
 *
 * Both numbers, because they answer different questions. The total is the shape
 * of the cluster; the unacknowledged count is the work. A distribution that
 * showed only totals would keep a fully-triaged cluster looking permanently
 * alarming, which is how a security page stops being read.
 */
export interface SeverityCount {
  severity: PostureSeverity
  total: number
  open: number
}

export function severityDistribution(findings: PostureFinding[]): SeverityCount[] {
  const counts = new Map<PostureSeverity, SeverityCount>(
    POSTURE_SEVERITIES.map((severity) => [severity, { severity, total: 0, open: 0 }]),
  )
  for (const finding of findings) {
    const entry = counts.get(severityOf(finding))
    if (!entry) continue
    entry.total += 1
    if (!finding.acknowledged) entry.open += 1
  }
  return POSTURE_SEVERITIES.map((severity) => counts.get(severity)!)
}

/** How the list is broken up. `none` keeps the server's own order, which is the
    ranking — so "no grouping" is a real choice rather than the absence of one. */
export type PostureGrouping = 'none' | 'severity' | 'namespace' | 'rule'

export interface PostureGroup {
  key: string
  label: string
  findings: PostureFinding[]
}

/**
 * Break the findings into the groups a reader asked for.
 *
 * Within every group the server's order is preserved rather than re-sorted:
 * `permits` descending is the ranking, and a group that re-ordered its own
 * contents alphabetically would bury the worst row in it.
 *
 * Grouping by namespace is the one that turns this page into something that can
 * be handed over — a namespace is usually a team, and "here is your list" is the
 * message a security review actually sends.
 */
export function groupFindings(findings: PostureFinding[], by: PostureGrouping): PostureGroup[] {
  if (by === 'none') {
    return [{ key: 'all', label: '', findings }]
  }

  const groups = new Map<string, PostureGroup>()
  for (const finding of findings) {
    const key = groupKey(finding, by)
    const existing = groups.get(key)
    if (existing) {
      existing.findings.push(finding)
      continue
    }
    groups.set(key, { key, label: key, findings: [finding] })
  }

  const out = [...groups.values()]
  if (by === 'severity') {
    // Severity groups run worst-first, which is the only order they have a
    // natural one in; alphabetical would put critical after high.
    out.sort((a, b) => POSTURE_SEVERITIES.indexOf(a.key as PostureSeverity) - POSTURE_SEVERITIES.indexOf(b.key as PostureSeverity))
    return out
  }
  // Namespace and rule groups lead with the group carrying the worst finding —
  // which, because the input is already ranked, is its first row.
  out.sort((a, b) => {
    const rank = (b.findings[0]?.permits ?? 0) - (a.findings[0]?.permits ?? 0)
    return rank !== 0 ? rank : a.label.localeCompare(b.label)
  })
  return out
}

function groupKey(finding: PostureFinding, by: PostureGrouping): string {
  switch (by) {
    case 'severity':
      return severityOf(finding)
    case 'namespace':
      // A cluster-scoped finding has no namespace, and saying so is better than
      // filing it under an empty string that reads as a namespace called "".
      return finding.namespace || 'cluster-scoped'
    case 'rule':
      return finding.title
    default:
      return 'all'
  }
}

/** What the list has been narrowed to. Every field is "no filter" when empty or
    null, so the default state is one object rather than a set of special cases. */
export interface PostureFilter {
  severity: PostureSeverity | null
  /** Whether acknowledged findings are drawn. Off by default: the work is what
      is left, and an acknowledged finding is a decision somebody already made
      and recorded. It is a filter rather than a deletion — the decision is still
      reviewable, which is the whole reason acknowledging carries a reason. */
  showAcknowledged: boolean
  /** Matched against the object's name, namespace, rule title and the field the
      finding names. Not the message: a substring search over prose matches
      almost everything and would make the box feel broken. */
  search: string
}

export const NO_POSTURE_FILTER: PostureFilter = {
  severity: null,
  showAcknowledged: false,
  search: '',
}

export function filterFindings(findings: PostureFinding[], filter: PostureFilter): PostureFinding[] {
  const needle = filter.search.trim().toLowerCase()
  return findings.filter((finding) => {
    if (!filter.showAcknowledged && finding.acknowledged) return false
    if (filter.severity && severityOf(finding) !== filter.severity) return false
    if (needle === '') return true
    return (
      finding.name.toLowerCase().includes(needle) ||
      (finding.namespace ?? '').toLowerCase().includes(needle) ||
      finding.title.toLowerCase().includes(needle) ||
      finding.field.toLowerCase().includes(needle)
    )
  })
}

/**
 * The findings as a CSV, for handing to the team that owns the fix.
 *
 * It is built in the browser from what is on screen, which is a deliberate
 * departure from the audit trail's export — that one is a server route, because
 * the trail is a table this server owns and re-reading it is free. A posture
 * scan is not: it is a live read of a cluster across every granted namespace
 * through the tunnel, so a server-side export would mean scanning the cluster a
 * second time to produce a file. That costs the customer's API server twice for
 * one answer, and worse, the second scan could disagree with the first — the
 * cluster moves. An export that does not match the screen it came off is not
 * evidence.
 *
 * So it exports exactly the rows in front of the reader, filtering included, and
 * the header row says which scope produced them.
 */
export function postureCsv(findings: PostureFinding[]): string {
  const rows = [POSTURE_CSV_COLUMNS.join(',')]
  for (const finding of findings) {
    rows.push(
      [
        severityOf(finding),
        String(finding.permits),
        finding.rule,
        finding.title,
        finding.kind,
        finding.namespace ?? '',
        finding.name,
        finding.container ?? '',
        finding.field,
        finding.message,
        // Whether this is a Pod Security Standards control is read from
        // `pss_covered` and never from the presence of a profile string — the
        // same rule the badge follows, so an export cannot classify a finding
        // differently from the row above it.
        finding.pss_covered ? `${finding.pss_profile} / ${finding.pss_control}` : 'not a PSS control',
        finding.acknowledged ? 'yes' : 'no',
        finding.ack_by ?? '',
        finding.ack_reason ?? '',
        finding.ack_at ?? '',
      ]
        .map(csvCell)
        .join(','),
    )
  }
  return rows.join('\n')
}

/** The column order is the order a finding is read out loud: how bad, what rule,
    what object, which field, and what was decided about it. */
export const POSTURE_CSV_COLUMNS = [
  'severity',
  'permits',
  'rule',
  'title',
  'kind',
  'namespace',
  'name',
  'container',
  'field',
  'message',
  'pss',
  'acknowledged',
  'ack_by',
  'ack_reason',
  'ack_at',
]

/** RFC 4180 quoting. A finding's message carries commas routinely and an
    acknowledgement reason is free text somebody typed, so this is not optional. */
function csvCell(value: string): string {
  if (value === '') return ''
  if (/[",\n\r]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`
  }
  return value
}

/** A filename that says what the file is of and when it was taken — a folder of
    `export.csv` is not evidence anybody can file. */
export function postureCsvFilename(cluster: string, namespace: string, now: Date): string {
  const scope = namespace && namespace !== 'all' ? namespace : 'all-namespaces'
  const stamp = now.toISOString().slice(0, 19).replace(/[:T]/g, '-')
  return `posture-${cluster}-${scope}-${stamp}.csv`
}
