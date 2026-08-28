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

/**
 * The soft pairing: a tinted plate with its own tone written on it. The
 * contrast pass measures every `tone on tone-soft` pairing at 4.5:1, which is
 * why text on one of these plates is always the *same* tone and never `fg` —
 * `fg` on a tint is a pairing nothing checks.
 */
export const TONE_SOFT: Record<Tone, string> = {
  ok: 'bg-ok-soft text-ok',
  warn: 'bg-warn-soft text-warn',
  bad: 'bg-danger-soft text-danger',
  idle: 'bg-raised text-muted',
  accent: 'bg-accent-soft text-accent',
}

export const TONE_FILL: Record<Tone, string> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-danger',
  idle: 'bg-faint',
  accent: 'bg-accent',
}

/**
 * A cluster's reachability has **one** derivation, and this is it.
 *
 * It used to have two. The pill read `cluster.status` — the last stored check,
 * which is a fact about some minute in the past — while every glyph read
 * `linkState`, which for an agent-mode cluster reads the live tunnel. On one
 * screen the dashboard said `Reachable · checked 9m ago` while the tree's dot
 * and the rail's chip were red, with the tunnel open at that moment. A console
 * whose single job is to answer "can this cluster be reached" cannot answer it
 * twice, and the older answer cannot be the one shown as today's.
 *
 * So the tone and the word are derived from the link below rather than from the
 * stored status, and the stored check keeps its own place: how old the reading
 * is, said as an age beside it, never as the state itself.
 */
const LINK_TONE: Record<LinkState, Tone> = {
  live: 'ok',
  direct: 'ok',
  down: 'bad',
  idle: 'idle',
}

export function clusterTone(cluster: Cluster): Tone {
  return LINK_TONE[linkState(cluster)]
}

export function clusterStateLabel(cluster: Cluster): string {
  const state = linkState(cluster)
  if (state === 'down') return 'Unreachable'
  if (state === 'idle') {
    // Two different nothings: an agent cluster that has not dialled in yet is
    // waiting on somebody else's terminal, and a direct one has simply never
    // been checked.
    return cluster.connection_mode === 'agent' ? 'Waiting to dial in' : 'Never checked'
  }
  return 'Reachable'
}

/** The four ways a link reads, wherever it is drawn. */
export type LinkState = 'live' | 'direct' | 'down' | 'idle'

/** How a cluster's link should read: is a tunnel carrying traffic right now. */
export function linkState(cluster: Cluster): LinkState {
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

/**
 * The tone of a status word a Kubernetes object reports — a namespace's phase,
 * a volume's, a claim's. It lives here rather than beside one table because two
 * surfaces now draw the same word for the same object: a namespace's row in the
 * list and its own page, and a phase that reads `ok` in one place and `idle` in
 * the other would be two answers to one question.
 *
 * `Granted` is the namespace list's own word for a scoped grant, which answers
 * from the grant rather than from the cluster — the namespace is there and
 * reachable, so it reads as an active one.
 */
export function phaseTone(phase: string): Tone {
  switch (phase) {
    case 'Bound':
    case 'Available':
    case 'Active':
    case 'Ready':
    case 'Granted':
      return 'ok'
    case 'Pending':
      return 'warn'
    case 'Failed':
    case 'Lost':
      return 'bad'
    default:
      return 'idle'
  }
}
