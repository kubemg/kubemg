import type { ShellState } from '../api/types'
import { formatTTL } from './time'

/*
 * What the shell page is looking at.
 *
 * The server answers one shape — the policy, plus the pod's state — and the page
 * has to turn that into one of a handful of very different screens: a feature
 * switched off, a cluster that cannot carry one, no shell yet, a shell coming
 * up, a shell to type into, or a shell that has ended. Deriving that here rather
 * than in the component is what lets it be asserted in a test instead of in a
 * browser, and it is the fact worth asserting: every one of these states has a
 * different button under it.
 */
export type ShellView =
  /** The operator switched the feature off server-wide. */
  | { kind: 'disabled'; reason: string }
  /** This cluster cannot carry one — a direct-mode registration, no image. */
  | { kind: 'unavailable'; reason: string }
  /** Nothing running. The ordinary state: a shell exists once somebody asks. */
  | { kind: 'absent' }
  /** A pod that is not a terminal yet, with the cluster's own reason if it has one. */
  | { kind: 'starting'; message?: string }
  /** Up, and safe to attach to. */
  | { kind: 'ready' }
  /** A pod that has finished. Its remains are cleared when the next one starts. */
  | { kind: 'ended'; message?: string }

export function shellView(state: ShellState | undefined): ShellView | null {
  if (!state) return null
  if (!state.enabled) {
    return { kind: 'disabled', reason: state.reason || 'The browser shell is switched off on this server.' }
  }
  if (!state.available) {
    return { kind: 'unavailable', reason: state.reason || 'A shell cannot be started on this cluster.' }
  }

  const pod = state.status
  if (!pod.exists) return { kind: 'absent' }
  if (pod.phase === 'Succeeded' || pod.phase === 'Failed') {
    return { kind: 'ended', message: pod.message }
  }
  if (pod.ready) return { kind: 'ready' }
  return { kind: 'starting', message: pod.message }
}

/**
 * What the shell can reach, in one sentence.
 *
 * It says the caller's own grant back to them, because that — not the pod — is
 * what bounds the terminal. Somebody who reads "you have view on two
 * namespaces" before typing does not spend ten minutes wondering why `kubectl
 * get pods -A` is refused.
 */
export function shellReach(state: ShellState): string {
  const role = state.k8s_role || 'view'
  const scope =
    state.namespaces && state.namespaces.length > 0
      ? `the ${state.namespaces.length === 1 ? 'namespace' : 'namespaces'} ${state.namespaces.join(', ')}`
      : 'every namespace on this cluster'
  return `Commands run as you, with ${role} on ${scope} — the cluster's own RBAC answers them.`
}

/**
 * The lifetime, said in the two clocks it actually has. Both are stated because
 * they fail in different directions and an operator who knows only the idle one
 * is surprised by the other.
 */
export function shellLifetime(state: ShellState): string {
  return (
    `It is reclaimed after ${formatTTL(state.idle_timeout_seconds)} without a keystroke, ` +
    `and ends after ${formatTTL(state.max_lifetime_seconds)} regardless. ` +
    `Nothing written inside it is kept.`
  )
}

/**
 * Seconds until a shell's absolute deadline, or null when there is nothing to
 * count down — no pod, or a pod carrying no deadline. A deadline already passed
 * reads as zero rather than as a negative number: the shell is over either way,
 * and a countdown that runs backwards past zero is a bug on screen.
 */
export function shellSecondsLeft(state: ShellState, now: number = Date.now()): number | null {
  const expiry = state.status.expires_at
  if (!state.status.exists || !expiry) return null
  const parsed = Date.parse(expiry)
  if (Number.isNaN(parsed)) return null
  return Math.max(0, Math.floor((parsed - now) / 1000))
}
