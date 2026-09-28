import type { Cluster } from '../api/types'
import { linkState } from '../lib/status'
import { PathHop, PathNode } from './LinkStatus'

/*
 * The path a call actually takes: cluster, tunnel, kubemg, proxy, you. This is
 * the product's thesis drawn as a picture rather than stated as a sentence, and
 * it earns that only by being drawn exactly once — the fleet page's own
 * heading (one row per cluster) and a single cluster's own masthead both call
 * this, rather than each keeping a copy that could drift from the other.
 *
 * Nothing here is a new read: every field comes off the `Cluster` object every
 * list and every dashboard already has.
 */
export function ConnectionChain({ cluster, username }: { cluster: Cluster; username: string }) {
  const viaAgent = cluster.connection_mode === 'agent'

  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:gap-4">
      <PathNode
        label="Cluster"
        value={cluster.name}
        tone={cluster.status === 'healthy' ? 'ok' : 'idle'}
      />
      <PathHop
        state={linkState(cluster)}
        caption={
          viaAgent
            ? cluster.agent_attached
              ? 'outbound tunnel · open'
              : 'outbound tunnel · not connected'
            : 'kubemg dials the API server'
        }
      />
      <PathNode label="kubemg" value={viaAgent ? 'bastion proxy' : 'token issuer'} tone="accent" />
      <PathHop
        state={viaAgent ? 'live' : 'direct'}
        label={viaAgent ? 'Proxied' : 'Kubeconfig'}
        caption={viaAgent ? 'proxied · audited' : 'kubeconfig · not proxied'}
      />
      <PathNode label="You" value={username} />
    </div>
  )
}
