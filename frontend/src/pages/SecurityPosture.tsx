import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { CheckCircle2, RefreshCw, ShieldAlert, ShieldCheck, Undo2 } from 'lucide-react'
import {
  acknowledgePostureFinding,
  errorMessage,
  fetchNamespaces,
  fetchClusterPosture,
  unacknowledgePostureFinding,
} from '../api/client'
import type { PostureFinding } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, EmptyState, Field, Notice, Pill, Select, TextArea } from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
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

  const loaded = scan.data
  const loading = scan.loading || scan.revalidating
  const findings = loaded?.findings ?? []
  const unacknowledged = findings.filter((f) => !f.acknowledged).length

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

        {/* The non-goal, stated rather than implied — this page must never be
            mistaken for a scanner it is not. */}
        {loaded ? <Notice tone="info">{loaded.non_goal_notice}</Notice> : null}

        {/* The PSS coverage gap, stated with the same weight as the non-goal
            notice above: citing Pod Security Standards for four rules must
            never read as "this checks baseline/restricted compliance". */}
        {loaded ? (
          <Notice tone="info">
            {loaded.pss_notice}
            {loaded.pss_unchecked.length > 0 ? (
              <>
                {' '}Not checked: {loaded.pss_unchecked.join('; ')}.
              </>
            ) : null}
          </Notice>
        ) : null}

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

        <div className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
            <h2 className="text-[14px] font-semibold text-fg">
              {findings.length} {findings.length === 1 ? 'finding' : 'findings'}
            </h2>
            {unacknowledged > 0 ? (
              <Pill tone="warn">{unacknowledged} unacknowledged</Pill>
            ) : findings.length > 0 ? (
              <Pill tone="ok">all acknowledged</Pill>
            ) : null}
            {loading ? <span className="text-[12px] text-muted">Reading the cluster…</span> : null}
          </div>

          {loading && !loaded ? <TableSkeleton columns={4} rows={8} label="Reading posture" /> : null}

          {!loading && loaded && findings.length === 0 ? (
            <p className="px-4 py-10 text-center text-[13px] text-muted">
              Nothing here trips one of the seven rules. That is the answer you want.
            </p>
          ) : null}

          {findings.length > 0 ? (
            <ul className="divide-y divide-line-soft">
              {findings.map((finding, i) => (
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
          ) : null}
        </div>

        <p className="text-[12px] text-muted">{loaded?.disclaimer}</p>
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
      <span
        aria-hidden="true"
        className={`mt-1.5 size-1.5 shrink-0 rounded-full ${finding.acknowledged ? 'bg-muted' : 'bg-danger'}`}
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
    <div className="fixed inset-0 z-40 flex items-end justify-center bg-black/40 p-4 sm:items-center">
      <form
        onSubmit={submit}
        className="w-full max-w-lg rounded-panel border border-line bg-panel p-4 shadow-xl"
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
