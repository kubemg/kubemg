import { useEffect, useState } from 'react'
import { errorMessage, fetchNetworkPolicyReachability } from '../api/client'
import type { Cluster, NetworkPolicyPeer, NetworkPolicyReachability } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { Notice, Pill } from './primitives'

/**
 * A workload's own view of the policies that select it — the derivation the
 * roadmap calls the part actually worth building, over the fixed-inventory
 * list beside it. Three things it answers: what may reach this workload, what
 * it may reach, and whether nothing selects it at all.
 *
 * It is a derivation from the NetworkPolicy objects alone, and the tab says so
 * on the surface itself rather than in a comment only this file's author would
 * ever read: `disclaimer` is rendered every time, because a reader who takes a
 * policy list for enforcement reality — on a cluster whose CNI ignores
 * NetworkPolicy entirely — has been misled by the console rather than by the
 * cluster.
 */
export function ReachabilityTab({
  cluster,
  kind,
  name,
  namespace,
}: {
  cluster: Cluster
  kind: ResourceKey
  name: string
  namespace: string
}) {
  const [result, setResult] = useState<NetworkPolicyReachability | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    fetchNetworkPolicyReachability(cluster.id, kind, name, namespace)
      .then((data) => {
        if (!cancelled) setResult(data)
      })
      .catch((err) => {
        if (!cancelled) setError(errorMessage(err, 'Could not derive reachability for this workload.'))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [cluster.id, kind, name, namespace])

  if (loading && !result) return <p className="text-[13px] text-muted">Reading NetworkPolicies…</p>
  if (error) return <Notice tone="error">{error}</Notice>
  if (!result) return null

  return (
    <div className="flex flex-col gap-4">
      <Notice tone="info">{result.disclaimer}</Notice>

      {!result.policies_available ? (
        <Notice tone="warn">
          {result.unavailable_reason ?? 'The NetworkPolicies in this namespace could not be read.'}
        </Notice>
      ) : (
        <>
          <Direction
            title="Reaching this workload (ingress)"
            covered={result.ingress_covered}
            namespaceHasPolicies={result.namespace_has_ingress_policies}
            policies={result.ingress_policies}
            peers={result.ingress_peers}
            peerLabel="May reach it from"
          />
          <Direction
            title="What this workload may reach (egress)"
            covered={result.egress_covered}
            namespaceHasPolicies={result.namespace_has_egress_policies}
            policies={result.egress_policies}
            peers={result.egress_peers}
            peerLabel="May reach out to"
          />

          {Object.keys(result.pod_labels).length > 0 ? (
            <div className="flex flex-col gap-2">
              <span className="label">
                Labels a policy matches against
                <span className="ml-1.5 text-faint">
                  ({result.label_source === 'pod' ? 'the pod itself' : 'its pod template'})
                </span>
              </span>
              <ul className="flex flex-wrap gap-1.5">
                {Object.entries(result.pod_labels)
                  .sort(([a], [b]) => a.localeCompare(b))
                  .map(([key, value]) => (
                    <li
                      key={key}
                      className="flex max-w-full min-w-0 items-center gap-1 rounded-chip border border-line bg-raised px-2 py-1 font-mono text-[12px]"
                    >
                      <span className="shrink-0 text-muted">{key}</span>
                      <span className="truncate text-fg">{value}</span>
                    </li>
                  ))}
              </ul>
              {/* A Deployment's pods can drift from its template after the
                  fact — a hand-applied patch, a mutating webhook — and a
                  policy matches whatever labels the running pod actually
                  carries. Saying which source this came from is what keeps
                  that gap honest rather than silently assumed away. */}
              {result.label_source === 'pod template' ? (
                <p className="text-[12px] text-muted">
                  Read off the pod template. A running pod can carry additional labels a webhook or a
                  manual change added afterwards — this does not see those.
                </p>
              ) : null}
            </div>
          ) : null}
        </>
      )}
    </div>
  )
}

function Direction({
  title,
  covered,
  namespaceHasPolicies,
  policies,
  peers,
  peerLabel,
}: {
  title: string
  covered: boolean
  namespaceHasPolicies: boolean
  policies: string[]
  peers: NetworkPolicyPeer[]
  peerLabel: string
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="label">{title}</span>
        {covered ? (
          <Pill tone="ok">Covered</Pill>
        ) : namespaceHasPolicies ? (
          // The finding the roadmap calls the most useful one: nothing selects
          // this workload, in a namespace where something else is governed —
          // wide open by omission rather than by decision.
          <Pill tone="bad">Wide open — no policy selects it here</Pill>
        ) : (
          <Pill tone="idle" dot={false}>
            No NetworkPolicy governs this direction in this namespace
          </Pill>
        )}
      </div>

      {covered ? (
        <>
          <p className="text-[12px] text-muted">
            Selected by {policies.length === 1 ? policies[0] : `${policies.length} policies`}
            {policies.length > 1 ? `: ${policies.join(', ')}` : ''}.
          </p>
          {peers.length === 0 ? (
            <p className="text-[12.5px] text-fg">Denies everything: no rule permits any peer.</p>
          ) : (
            <ul className="flex flex-col gap-1.5">
              <li className="text-[12px] text-faint">{peerLabel}:</li>
              {peers.map((peer, index) => (
                <li
                  key={index}
                  className="rounded-control border border-line bg-raised px-2.5 py-1.5 font-mono text-[12.5px] text-fg"
                >
                  <PeerText peer={peer} />
                </li>
              ))}
            </ul>
          )}
        </>
      ) : null}
    </div>
  )
}

/** Where a "pod" peer's namespace comes from — whichever of the two is set. */
function podPeerScope(peer: NetworkPolicyPeer): string {
  if (peer.namespace) return peer.namespace
  if (peer.namespace_selector) return `namespaces matching ${peer.namespace_selector}`
  return 'this namespace'
}

function PeerText({ peer }: { peer: NetworkPolicyPeer }) {
  switch (peer.kind) {
    case 'all':
      return <>everything</>
    case 'ip_block':
      return (
        <>
          {peer.cidr}
          {peer.except && peer.except.length > 0 ? <> except {peer.except.join(', ')}</> : null}
        </>
      )
    case 'namespace':
      return <>namespaces matching {peer.namespace_selector || 'all namespaces'}</>
    case 'pod':
      return (
        <>
          pods matching {peer.pod_selector || 'all pods'} in {podPeerScope(peer)}
        </>
      )
  }
}
