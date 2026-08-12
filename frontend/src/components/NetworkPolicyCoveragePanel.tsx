import { useEffect, useState } from 'react'
import { errorMessage, fetchNetworkPolicyCoverage } from '../api/client'
import type { Cluster, NetworkPolicyCoverage } from '../api/types'
import { Notice, Pill } from './primitives'

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

  return (
    <div className="card flex flex-col gap-2.5 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium text-fg">
          Coverage in <span className="font-mono">{coverage.namespace}</span>
        </span>
        <span className="font-mono text-[12px] text-faint">
          {coverage.policy_count} {coverage.policy_count === 1 ? 'policy' : 'policies'},{' '}
          {coverage.pod_count} {coverage.pod_count === 1 ? 'pod' : 'pods'}
        </span>
      </div>

      <div className="flex flex-wrap gap-4">
        <CoverageReading
          label="Ingress"
          covered={coverage.ingress_covered_pods}
          uncovered={coverage.ingress_uncovered_pods}
          examples={coverage.ingress_uncovered_examples}
        />
        <CoverageReading
          label="Egress"
          covered={coverage.egress_covered_pods}
          uncovered={coverage.egress_uncovered_pods}
          examples={coverage.egress_uncovered_examples}
        />
      </div>

      <p className="text-[12px] text-muted">{coverage.disclaimer}</p>
    </div>
  )
}

function CoverageReading({
  label,
  covered,
  uncovered,
  examples,
}: {
  label: string
  covered: number
  uncovered: number
  examples?: string[]
}) {
  return (
    <div className="flex min-w-40 flex-col gap-1">
      <span className="text-[11px] tracking-wide text-faint uppercase">{label}</span>
      <div className="flex items-center gap-1.5">
        <Pill tone="ok" dot={false}>
          {covered} covered
        </Pill>
        {uncovered > 0 ? (
          // `covered === 0` is read as "nothing here uses a NetworkPolicy for
          // this direction at all" rather than as "everything is wide open by
          // omission" — the sharper finding this whole feature exists to
          // surface is a namespace where *some* pods are governed and others
          // are not, which is exactly what a non-zero `covered` alongside a
          // non-zero `uncovered` means.
          <Pill tone={covered === 0 ? 'idle' : 'bad'} dot={false}>
            {uncovered} uncovered
          </Pill>
        ) : null}
      </div>
      {uncovered > 0 && examples && examples.length > 0 ? (
        <span className="truncate font-mono text-[11.5px] text-faint" title={examples.join(', ')}>
          e.g. {examples.slice(0, 3).join(', ')}
          {uncovered > examples.length ? ` +${uncovered - examples.length} more` : ''}
        </span>
      ) : null}
    </div>
  )
}
