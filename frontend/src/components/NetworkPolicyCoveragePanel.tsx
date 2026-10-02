import { useEffect, useState } from 'react'
import { errorMessage, fetchNetworkPolicyCoverage } from '../api/client'
import { ArrowDownToLine, ArrowUpFromLine } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import type { Cluster, NetworkPolicyCoverage } from '../api/types'
import { Notice, Panel } from './primitives'

/**
 * The namespace-level summary of what is and is not covered by a
 * NetworkPolicy — the other half of the roadmap item, over the NetworkPolicies
 * list itself rather than over one workload. A policy count says how many
 * objects exist; this says how many of the *pods actually running here* are
 * governed, which is the question the list alone cannot answer and the one an
 * auditor actually opens this page with.
 *
 * A NetworkPolicy never reaches across a namespace boundary, so "coverage" is
 * only ever a single-namespace question — this panel does not attempt an
 * all-namespaces rollup, and says so rather than silently doing nothing when
 * "All namespaces" is selected.
 */
export function NetworkPolicyCoveragePanel({
  cluster,
  namespace,
}: {
  cluster: Cluster
  namespace: string
}) {
  const [coverage, setCoverage] = useState<NetworkPolicyCoverage | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setCoverage(null)
    setError(null)

    fetchNetworkPolicyCoverage(cluster.id, namespace)
      .then((data) => {
        if (!cancelled) setCoverage(data)
      })
      .catch((err) => {
        if (!cancelled) setError(errorMessage(err, 'Could not read NetworkPolicy coverage.'))
      })

    return () => {
      cancelled = true
    }
  }, [cluster.id, namespace])

  if (error) return <Notice tone="error">{error}</Notice>
  if (!coverage) return null

  if (!coverage.available) {
    return (
      <Notice tone="warn">
        {coverage.unavailable_reason ?? 'Coverage could not be read for this namespace.'}
      </Notice>
    )
  }

  if (coverage.pod_count === 0) {
    return null
  }

  const pods = `${coverage.pod_count} ${coverage.pod_count === 1 ? 'pod' : 'pods'} running`
  const policies =
    coverage.policy_count === 0
      ? `No NetworkPolicy in ${coverage.namespace}`
      : `${coverage.policy_count} ${coverage.policy_count === 1 ? 'NetworkPolicy' : 'NetworkPolicies'} in ${coverage.namespace}`

  return (
    <Panel
      title="Network policy coverage"
      eyebrow="Declared"
      description={`${policies}, ${pods}. Which of them a policy selects, in each direction.`}
    >
      <div className="grid gap-4 p-5 md:grid-cols-2">
        <CoverageReading
          icon={ArrowDownToLine}
          label="Ingress"
          covered={coverage.ingress_covered_pods}
          uncovered={coverage.ingress_uncovered_pods}
          examples={coverage.ingress_uncovered_examples}
        />
        <CoverageReading
          icon={ArrowUpFromLine}
          label="Egress"
          covered={coverage.egress_covered_pods}
          uncovered={coverage.egress_uncovered_pods}
          examples={coverage.egress_uncovered_examples}
        />
      </div>
      <p className="border-t border-line-soft px-5 py-3.5 text-[12.5px] leading-relaxed text-muted">
        {coverage.disclaimer}
      </p>
    </Panel>
  )
}

function CoverageReading({
  icon: Icon,
  label,
  covered,
  uncovered,
  examples,
}: {
  icon: LucideIcon
  label: string
  covered: number
  uncovered: number
  examples?: string[]
}) {
  // `covered === 0` is read as "nothing here uses a NetworkPolicy for this
  // direction at all" rather than as "everything is wide open by omission" —
  // the sharper finding this whole feature exists to surface is a namespace
  // where *some* pods are governed and others are not, which is exactly what a
  // non-zero `covered` alongside a non-zero `uncovered` means. So the gap is
  // drawn as danger only then, and as the quiet tone otherwise.
  const gap = covered === 0 ? 'bg-faint' : 'bg-danger'
  const gapText = covered === 0 ? 'text-fg' : 'text-danger'
  const total = covered + uncovered
  // The "more" counts against what is drawn, not against what was sent: the
  // server may send more examples than the three shown here.
  const shown = examples?.slice(0, 3) ?? []

  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-card border border-line-soft bg-raised/40 p-4">
      <div className="flex items-center gap-3">
        <span className="grid size-9 shrink-0 place-items-center rounded-full border border-line-soft bg-surface text-muted shadow-deck">
          <Icon aria-hidden="true" className="size-4" />
        </span>
        <span className="text-[14px] font-semibold text-fg">{label}</span>
        <span className="ml-auto text-[12.5px] text-muted">
          <span className="font-data font-semibold text-fg tabular-nums">{covered}</span> of{' '}
          <span className="font-data tabular-nums">{total}</span> covered
        </span>
      </div>

      <div
        role="img"
        aria-label={`${covered} covered, ${uncovered} uncovered`}
        className="flex h-2.5 gap-[3px]"
      >
        {covered > 0 ? (
          <span className="block h-full rounded-[3px] bg-ok" style={{ flexGrow: covered }} />
        ) : null}
        {uncovered > 0 ? (
          <span className={`block h-full rounded-[3px] ${gap}`} style={{ flexGrow: uncovered }} />
        ) : null}
      </div>

      <ul className="flex flex-wrap gap-x-5 gap-y-1 text-[13px] text-muted">
        <li className="flex items-center gap-2">
          <span aria-hidden="true" className="size-2.5 rounded-full bg-ok" />
          Covered
          <span className="font-data font-semibold text-fg tabular-nums">{covered}</span>
        </li>
        <li className="flex items-center gap-2">
          <span aria-hidden="true" className={`size-2.5 rounded-full ${gap}`} />
          Uncovered
          <span className={`font-data font-semibold tabular-nums ${uncovered > 0 ? gapText : 'text-fg'}`}>
            {uncovered}
          </span>
        </li>
      </ul>

      {uncovered > 0 && shown.length > 0 ? (
        <p className="min-w-0 text-[12.5px] text-muted">
          <span className="text-faint">Not selected: </span>
          <span className="font-data text-fg [overflow-wrap:anywhere]" title={examples?.join(', ')}>
            {shown.join(', ')}
          </span>
          {uncovered > shown.length ? ` and ${uncovered - shown.length} more` : ''}
        </p>
      ) : null}
    </div>
  )
}
