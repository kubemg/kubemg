import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { CheckCircle2, Download, RefreshCw, ShieldAlert, ShieldCheck, Undo2 } from 'lucide-react'
import {
  acknowledgePostureFinding,
  errorMessage,
  fetchNamespaces,
  fetchClusterPosture,
  unacknowledgePostureFinding,
} from '../api/client'
import type { PostureFinding } from '../api/types'
import { AppShell } from '../components/AppShell'
import { SEVERITY_STYLE, SeverityStrip, SeverityTag } from '../components/SeverityStrip'
import {
  Button,
  EmptyState,
  Field,
  Notice,
  Pill,
  SearchInput,
  Segmented,
  Select,
  TextArea,
} from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import type { PostureGrouping, PostureSeverity } from '../lib/posture'
import {
  NO_POSTURE_FILTER,
  filterFindings,
  groupFindings,
  postureCsv,
  postureCsvFilename,
  severityDistribution,
  severityOf,
} from '../lib/posture'
import { ALL_NAMESPACES } from '../lib/resources'
import { queryKey, useCachedQuery } from '../lib/query'
import { relativeAge } from '../lib/time'
import { useClusters } from '../state/clusters-context'

/**
 * Workload security posture, derived from what Explore already reads.
 *
 * Every finding here is a fact the manifest already declares — a privileged
 * container, a hostPath mount, a namespace with no NetworkPolicy — read
 * through the same agent tunnel and the same grant as everything else in
 * Explore. **This is not a vulnerability scanner**: KubeMG holds no image
 * registry credential and no CVE feed, and nothing here looks inside a
 * container image. An image's known vulnerabilities belong to whatever already
 * scans your registry — if this cluster has registered one (see the cluster's
 * consoles), a finding's image links there; otherwise it is named as plain
 * text rather than a guess.
 *
 * Findings are ordered by what they **permit**, not by how many fired — a
 * container missing a memory limit sorts well below one running privileged
 * with hostPID, regardless of which namespace has more of either.
 *
 * "Opens the object" here means what EventsTimeline's equivalent link already
 * means: a jump into Explore, filtered to the namespace the object lives in,
 * with the resource kind selected. It does not scroll to the specific field —
 * Explore's drawer has no such anchor — so the finding names the field in its
 * own text instead of implying a highlight that is not there.
 *
 * Four of the seven rules are named Kubernetes Pod Security Standards
 * controls; three are not, and PSS does not address what they check at all.
 * `PSSBadge` renders that distinction on every finding rather than only on
 * the ones that cite something, on the same principle `non_goal_notice`
 * already establishes for the whole page: a reader must be told what is
 * *not* being claimed, not just what is. `pss_notice` and `pss_unchecked`
 * carry the coarser version of the same statement — this checks four
 * controls, not the two profiles — in a Notice next to the existing
 * non-goal one, never folded into it, since the two are separate claims.
 */

const NS_PARAM = 'ns'

const KIND_TO_RESOURCE: Record<string, string> = {
  Pod: 'pods',
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}

export function SecurityPosture() {
  const { clusters, loading: clustersLoading } = useClusters()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)

  const [searchParams, setSearchParams] = useSearchParams()
  const namespace = searchParams.get(NS_PARAM) ?? ALL_NAMESPACES

  const reachable = useMemo(
    () => clusters.filter((entry) => entry.connection_mode === 'agent' && entry.agent_attached),
    [clusters],
  )
  const cluster = reachable.find((entry) => entry.id === clusterId) ?? null
  const unreachable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  const namespaces = useCachedQuery(
    cluster ? queryKey('namespaces', cluster.id) : null,
    () => fetchNamespaces(cluster!.id),
  )

  const scanKey = cluster ? queryKey('posture', cluster.id, namespace) : null
  const scan = useCachedQuery(scanKey, async () => {
    if (!cluster) throw new Error('no cluster is selected')
    return fetchClusterPosture(cluster.id, namespace)
  })

  const [acking, setAcking] = useState<PostureFinding | null>(null)
  const [filter, setFilter] = useState(NO_POSTURE_FILTER)
  const [grouping, setGrouping] = useState<PostureGrouping>('none')

  /* Every derivation below is a hook, so all of them run before the early
     return for an unreachable cluster — a `useMemo` after a conditional return
     is a different hook order on the two paths, which React cannot survive. */
  const loaded = scan.data
  const findings = useMemo(() => loaded?.findings ?? [], [loaded])
  const distribution = useMemo(() => severityDistribution(findings), [findings])
  const visible = useMemo(() => filterFindings(findings, filter), [findings, filter])
  const groups = useMemo(() => groupFindings(visible, grouping), [visible, grouping])

  function setNamespace(value: string) {
    setSearchParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        next.set(NS_PARAM, value)
        return next
      },
      { replace: true },
    )
  }

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Security posture">
        <div className="card">
          <EmptyState
            icon={<ShieldAlert aria-hidden="true" className="size-5" />}
            title={
              unreachable ? `${unreachable.name} has no live connection` : 'That cluster is not registered'
            }
          >
            {unreachable ? (
              <>
                Posture is derived on demand through the agent tunnel, so a cluster without one has
                nothing to show.{' '}
                <Link to={`/clusters/${unreachable.id}/summary`} className="text-accent hover:underline">
                  Open the cluster
                </Link>{' '}
                to check its connection.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read its posture.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const loading = scan.loading || scan.revalidating
  const unacknowledged = findings.filter((f) => !f.acknowledged).length

  /* The export is built here, in the browser, from the rows in front of the
     reader — filtering included. That is a deliberate departure from the audit
     trail's export, which is a server route: the trail is a table this server
     owns and re-reading it is free, whereas a posture scan is a live read of a
     cluster across every granted namespace through the tunnel. A server-side
     export would scan the cluster a second time to produce a file, which costs
     the customer's API server twice for one answer and could disagree with the
     screen it came off, because the cluster moves. An export that does not match
     what was on screen is not evidence. */
  function exportCsv() {
    if (!cluster) return
    const blob = new Blob([postureCsv(visible)], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = postureCsvFilename(cluster.name, namespace, new Date())
    link.click()
    URL.revokeObjectURL(url)
  }

  return (
    <AppShell
      title="Security posture"
      fullWidth
      scope={
        cluster ? (
          <div className="w-56">
            <Select
              aria-label="Namespace"
              size="sm"
              value={namespace}
              disabled={namespaces.data === undefined}
              onChange={(event) => setNamespace(event.target.value)}
            >
              <option value={ALL_NAMESPACES}>
                {namespaces.data?.scoped ? 'All granted namespaces' : 'All namespaces'}
              </option>
              {(namespaces.data?.namespaces ?? []).map((entry) => (
                <option key={entry.name} value={entry.name}>
                  {entry.name}
                </option>
              ))}
            </Select>
          </div>
        ) : undefined
      }
      actions={
        <Button onClick={() => void scan.refresh()} disabled={loading}>
          <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
          Refresh
        </Button>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {scan.error ? (
          <Notice tone="error">{errorMessage(scan.error, 'Could not read this cluster’s posture.')}</Notice>
        ) : null}

        {/* Folded, not cut. Both notices used to open the page: two grey
            paragraphs, the second of them six lines of semicolon-separated PSS
            control names with a bare URL in it, before a single finding — so a
            security team was shown the limits of the scan before the scan.
            Every word is still here and one click away, because both are real
            claims about what is and is not being checked; what changed is that
            the findings now come first. See ScopeDisclosure. */}
        {loaded ? <ScopeDisclosure view={loaded} /> : null}

        {loaded?.unavailable?.length ? (
          <Notice tone="warn">
            Some of what this scan reads from could not be read:{' '}
            {loaded.unavailable.map((gap) => `${gap.resource} (${gap.reason})`).join('; ')}. The findings
            below are narrowed to what could be read, not a complete answer for this scope.
          </Notice>
        ) : null}

        {loaded?.truncated || loaded?.findings_capped ? (
          <Notice tone="warn">
            This scan hit its bound —{' '}
            {loaded.scanned_workloads + loaded.scanned_pods} objects were evaluated
            {loaded.findings_capped ? ' and the finding list itself was capped' : ''}. Narrow to one
            namespace for a complete answer over a smaller scope.
          </Notice>
        ) : null}

        {/* What the cluster looks like, before what to do about it. The bands
            double as the severity filter: a distribution nobody can act on is a
            decoration, and the row a reader wants after seeing "3 critical" is
            those three. */}
        {findings.length > 0 ? (
          <SeverityStrip
            distribution={distribution}
            selected={filter.severity}
            onSelect={(severity) =>
              setFilter((current) => ({
                ...current,
                severity: current.severity === severity ? null : severity,
              }))
            }
          />
        ) : null}

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">
              {visible.length === findings.length
                ? `${findings.length} ${findings.length === 1 ? 'finding' : 'findings'}`
                : `${visible.length} of ${findings.length} findings`}
            </h2>
            {unacknowledged > 0 ? (
              <Pill tone="warn">{unacknowledged} unacknowledged</Pill>
            ) : findings.length > 0 ? (
              <Pill tone="ok">all acknowledged</Pill>
            ) : null}
            {loading ? <span className="text-[12px] text-muted">Reading the cluster…</span> : null}

            <div className="ml-auto flex flex-wrap items-center gap-2">
              <SearchInput
                value={filter.search}
                onChange={(search) => setFilter((current) => ({ ...current, search }))}
                label="Filter findings"
                placeholder="Object, namespace, rule or field"
              />
              <Segmented<PostureGrouping>
                ariaLabel="Group findings"
                value={grouping}
                onChange={setGrouping}
                options={[
                  { value: 'none', label: 'Ranked' },
                  { value: 'severity', label: 'Severity' },
                  { value: 'namespace', label: 'Namespace' },
                  { value: 'rule', label: 'Rule' },
                ]}
              />
              {/* A filter rather than a deletion: an acknowledged finding is a
                  decision somebody recorded with a reason, and it stays
                  reviewable. It is off by default because the work is what is
                  left. */}
              <label className="flex shrink-0 items-center gap-1.5 text-[12.5px] text-muted">
                <input
                  type="checkbox"
                  checked={filter.showAcknowledged}
                  onChange={(event) =>
                    setFilter((current) => ({ ...current, showAcknowledged: event.target.checked }))
                  }
                  className="size-3.5 accent-[var(--deck-accent-fill)]"
                />
                Acknowledged
              </label>
              <Button size="sm" onClick={exportCsv} disabled={visible.length === 0}>
                <Download aria-hidden="true" className="size-3.5" />
                Export
              </Button>
            </div>
          </div>

          {loading && !loaded ? <TableSkeleton columns={4} rows={8} label="Reading posture" /> : null}

          {!loading && loaded && findings.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              Nothing here trips one of the seven rules. That is the answer you want.
            </p>
          ) : null}

          {findings.length > 0 && visible.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              Nothing matches this filter. {findings.length} finding
              {findings.length === 1 ? '' : 's'} {findings.length === 1 ? 'is' : 'are'} hidden by it.
            </p>
          ) : null}

          {groups.map((group) => (
            <section key={group.key}>
              {group.label ? (
                <h3 className="flex items-center gap-2 border-b border-line-soft bg-raised px-4 py-1.5">
                  {grouping === 'severity' ? (
                    <SeverityTag severity={group.key as PostureSeverity} />
                  ) : (
                    <span className="min-w-0 truncate font-mono text-[12.5px] text-fg">
                      {group.label}
                    </span>
                  )}
                  <span className="font-mono text-[11.5px] text-faint">{group.findings.length}</span>
                </h3>
              ) : null}
              <ul className="divide-y divide-line-soft">
                {group.findings.map((finding, i) => (
                  <FindingRow
                    key={`${finding.kind}/${finding.namespace ?? ''}/${finding.name}/${finding.rule}/${finding.container ?? ''}/${i}`}
                    clusterId={cluster!.id}
                    finding={finding}
                    onAcknowledge={() => setAcking(finding)}
                    onUnacknowledge={async () => {
                      await unacknowledgePostureFinding(cluster!.id, finding)
                      await scan.refresh()
                    }}
                  />
                ))}
              </ul>
            </section>
          ))}
        </div>
      </div>

      {acking ? (
        <AcknowledgeSheet
          clusterId={cluster!.id}
          finding={acking}
          onClose={() => setAcking(null)}
          onSaved={async () => {
            setAcking(null)
            await scan.refresh()
          }}
        />
      ) : null}
    </AppShell>
  )
}

/**
 * What this scan checks, and what it does not — folded.
 *
 * Every word that used to open the page is still here: the non-goal notice, the
 * PSS coverage statement with its full list of unchecked controls, and the
 * derivation disclaimer. None of it was cut, because all three are claims about
 * what is *not* being asserted, and dropping any of them would leave the page
 * quietly overstating itself.
 *
 * What changed is the order. A security team is here to read findings; being
 * shown the limits of the scan before the scan is what made the page feel like
 * a disclaimer with a list attached. Closed by default, one line, one click.
 */
function ScopeDisclosure({
  view,
}: {
  view: { non_goal_notice: string; pss_notice: string; pss_unchecked: string[]; disclaimer: string }
}) {
  return (
    <details className="card px-4 py-3">
      <summary className="cursor-pointer text-[12.5px] text-muted">
        What this checks, and what it does not
      </summary>
      <div className="mt-3 flex flex-col gap-3 text-[12.5px] leading-relaxed text-muted">
        <p>{view.non_goal_notice}</p>
        <p>{view.pss_notice}</p>
        {view.pss_unchecked.length > 0 ? (
          <p>
            <span className="label text-faint">Not checked</span>
            <br />
            {view.pss_unchecked.join("; ")}.
          </p>
        ) : null}
        <p>{view.disclaimer}</p>
      </div>
    </details>
  )
}

/**
 * PSSBadge renders a finding's Pod Security Standards classification —
 * `pss_covered` decides which of the two mutually exclusive shapes to show,
 * never the presence of `pss_profile`/`pss_note` themselves, so this can
 * never render a covered finding as uncovered or the reverse. A covered
 * finding names its profile and control; an uncovered one says so plainly,
 * with the reason in its title tooltip rather than inline, on the same
 * pattern ClusterState already uses for a status message that is secondary
 * to the pill's own label.
 */
function PSSBadge({ finding }: { finding: PostureFinding }) {
  if (finding.pss_covered) {
    return (
      <Pill tone={finding.pss_profile === 'restricted' ? 'accent' : 'idle'} title={finding.pss_control}>
        PSS {finding.pss_profile} · {finding.pss_control}
      </Pill>
    )
  }
  return (
    <Pill tone="idle" dot={false} title={finding.pss_note}>
      Not a PSS control
    </Pill>
  )
}

/** One finding: what it permits, what field produced it, and where it is. */
function FindingRow({
  clusterId,
  finding,
  onAcknowledge,
  onUnacknowledge,
}: {
  clusterId: number
  finding: PostureFinding
  onAcknowledge: () => void
  onUnacknowledge: () => Promise<void>
}) {
  const resource = KIND_TO_RESOURCE[finding.kind]
  const [busy, setBusy] = useState(false)

  return (
    <li className="flex flex-wrap items-start gap-3 px-4 py-3">
      {/* The stripe carries the severity, where the old dot carried only
          "acknowledged or not" — every unacknowledged finding was the same red,
          which is what made 36 rows read as one undifferentiated wall. An
          acknowledged row keeps its band and loses its saturation: the finding
          is no less severe for having been accepted, and drawing it as though it
          were would hide what somebody signed off on. */}
      <span
        aria-hidden="true"
        title={SEVERITY_STYLE[severityOf(finding)].label}
        className={`mt-1 h-8 w-1 shrink-0 rounded-full ${SEVERITY_STYLE[severityOf(finding)].bar} ${
          finding.acknowledged ? 'opacity-30' : ''
        }`}
      />

      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-[13.5px] font-medium text-fg">{finding.title}</span>
          <span className="text-[11px] text-faint">{finding.kind}</span>
          <span className="truncate font-mono text-[13px] text-fg">
            {finding.namespace ? <span className="text-faint">{finding.namespace}/</span> : null}
            {finding.name}
          </span>
          {finding.container ? (
            <span className="font-mono text-[11.5px] text-faint">{finding.container}</span>
          ) : null}
          <PSSBadge finding={finding} />
          {finding.acknowledged ? <Pill tone="idle">Acknowledged</Pill> : null}
        </div>

        <p className="mt-1 max-w-3xl text-[12.5px] leading-relaxed text-muted">{finding.message}</p>
        <p className="mt-1 font-mono text-[11.5px] text-faint">{finding.field}</p>

        {finding.acknowledged ? (
          <p className="mt-1 text-[12px] text-muted">
            {finding.ack_by} — {finding.ack_reason}
            {finding.ack_at ? <> · {relativeAge(finding.ack_at)}</> : null}
          </p>
        ) : null}

        {resource ? (
          <Link
            to={`/clusters/${clusterId}/explore/${resource}${
              finding.namespace ? `?ns=${encodeURIComponent(finding.namespace)}` : ''
            }`}
            className="mt-1 inline-block text-[12px] text-accent hover:underline"
          >
            Open {finding.namespace || 'this'} in Explore
          </Link>
        ) : null}
      </div>

      <div className="shrink-0">
        {finding.acknowledged ? (
          <Button
            size="sm"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              try {
                await onUnacknowledge()
              } finally {
                setBusy(false)
              }
            }}
          >
            <Undo2 aria-hidden="true" className="size-3.5" />
            Unacknowledge
          </Button>
        ) : (
          <Button size="sm" onClick={onAcknowledge}>
            <CheckCircle2 aria-hidden="true" className="size-3.5" />
            Acknowledge
          </Button>
        )}
      </div>
    </li>
  )
}

/** The acknowledgement form: a finding trips a rule on purpose, and this is
    the way to say so — with a reason, since that is what makes it an
    audit-able decision rather than a mute button. */
function AcknowledgeSheet({
  clusterId,
  finding,
  onClose,
  onSaved,
}: {
  clusterId: number
  finding: PostureFinding
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const [reason, setReason] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSaving(true)
    try {
      await acknowledgePostureFinding(clusterId, finding, reason.trim())
      setError(null)
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save that acknowledgement.'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-40 flex items-end justify-center p-4 sm:items-center">
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="scrim-in absolute inset-0 bg-black/55 backdrop-blur-[2px]"
      />

      <form
        role="dialog"
        aria-modal="true"
        aria-label="Acknowledge this finding"
        onSubmit={submit}
        className="pop-in card relative w-full max-w-lg p-4 lift"
      >
        <div className="mb-3 flex items-center gap-2">
          <ShieldCheck aria-hidden="true" className="size-4 text-accent" />
          <h2 className="text-[14px] font-semibold text-fg">Acknowledge this finding</h2>
        </div>

        <p className="mb-3 text-[12.5px] leading-relaxed text-muted">
          {finding.title} on{' '}
          <span className="font-mono text-fg">
            {finding.namespace ? `${finding.namespace}/` : ''}
            {finding.name}
          </span>
          . The finding stays on every future scan, marked as acknowledged with your name and this
          reason — it does not disappear.
        </p>

        {error ? (
          <div className="mb-3">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        <Field label="Reason" htmlFor="ack-reason" hint="Why this is here on purpose.">
          <TextArea
            id="ack-reason"
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder="e.g. runs privileged on purpose to drive the hardware test rig in this namespace"
            autoFocus
          />
        </Field>

        <div className="mt-4 flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={saving || reason.trim() === ''}>
            {saving ? 'Saving…' : 'Acknowledge'}
          </Button>
        </div>
      </form>
    </div>
  )
}
