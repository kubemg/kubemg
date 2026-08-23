import type { Cluster, Pod, Workload } from '../api/types'

/** The five tones the deck reads state in. Nothing is ever colour alone. */
export type Tone = 'ok' | 'warn' | 'bad' | 'idle' | 'accent'

export const TONE_TEXT: Record<Tone, string> = {
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-danger',
  idle: 'text-faint',
  accent: 'text-accent',
}

export const TONE_FILL: Record<Tone, string> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-danger',
  idle: 'bg-faint',
  accent: 'bg-accent',
}

export function clusterTone(cluster: Cluster): Tone {
  if (cluster.status === 'healthy') return 'ok'
  if (cluster.status === 'unhealthy') return 'bad'
  return 'idle'
}

export function clusterStateLabel(cluster: Cluster): string {
  if (cluster.status === 'healthy') return 'Reachable'
  if (cluster.status === 'unhealthy') return 'Unreachable'
  return 'Never checked'
}

/** How a cluster's link should read: is a tunnel carrying traffic right now. */
export function linkState(cluster: Cluster): 'live' | 'direct' | 'down' | 'idle' {
  if (cluster.connection_mode === 'agent') {
    if (cluster.agent_attached) return 'live'
    return cluster.status === 'unhealthy' ? 'down' : 'idle'
  }
  if (cluster.status === 'healthy') return 'direct'
  return cluster.status === 'unhealthy' ? 'down' : 'idle'
}

export function podTone(pod: Pod): Tone {
  if (pod.phase === 'Running' && pod.ready === pod.total) return 'ok'
  if (pod.phase === 'Succeeded') return 'idle'
  if (pod.phase === 'Failed') return 'bad'
  return 'warn'
}

export function workloadTone(workload: Workload): Tone {
  if (workload.desired > 0 && workload.ready === workload.desired) return 'ok'
  if (workload.ready === 0) return 'bad'
  return 'warn'
}
