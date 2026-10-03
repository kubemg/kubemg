import type { Alarm, AlarmCondition, FiringAlert, ObservabilitySource, Pod, SilenceDuration } from '../api/types'
import type { Tone } from './status'

/*
 * Alarms on an object, and what the cluster's Alertmanager says about it.
 *
 * Pure: no React, no fetch. The server owns the catalogue and writes every
 * expression — this module only decides what the form opens on and how a
 * reading is worded and toned.
 */

/** The kinds the server's catalogue has conditions for. */
export const ALARM_KINDS: ReadonlySet<string> = new Set([
  'deployments',
  'statefulsets',
  'daemonsets',
  'pods',
  'jobs',
  'cronjobs',
  'persistentvolumeclaims',
])

export function supportsAlarms(kind: string): boolean {
  return ALARM_KINDS.has(kind)
}

/**
 * Whether a cluster alerts at all: an Alertmanager registered and switched on.
 * Without one an alarm cannot be created, so the drawer offers none of it.
 */
export function hasAlerting(sources: ObservabilitySource[] | undefined): boolean {
  return Boolean(sources?.some((source) => source.kind === 'alerts' && source.enabled))
}

export const SILENCE_DURATIONS: { value: SilenceDuration; label: string }[] = [
  { value: '1h', label: '1 hour' },
  { value: '4h', label: '4 hours' },
  { value: '12h', label: '12 hours' },
  { value: '1d', label: '1 day' },
  { value: '3d', label: '3 days' },
  { value: '7d', label: '7 days' },
]

const DURATION_WORDS: Record<string, string> = {
  '0m': 'at once',
  '1m': 'for 1 minute',
  '5m': 'for 5 minutes',
  '10m': 'for 10 minutes',
  '15m': 'for 15 minutes',
  '30m': 'for 30 minutes',
  '1h': 'for 1 hour',
}

/** How a rule's `for` reads in a sentence. */
export function forWords(duration: string): string {
  return DURATION_WORDS[duration] ?? `for ${duration}`
}

interface ConditionLike {
  type: string
  status: string
}

/**
 * The condition the form should open on: the one that describes what is wrong
 * with the object right now, so "create an alarm for this" means *this*. With
 * nothing wrong, it is the kind's first entry.
 */
export function suggestCondition(
  kind: string,
  available: AlarmCondition[],
  conditions: ConditionLike[] = [],
  pod?: Pod,
): string | undefined {
  const has = (key: string) => available.some((entry) => entry.key === key)
  const failing = (type: string) =>
    conditions.some((condition) => condition.type === type && condition.status === 'False')

  let pick: string | undefined
  if (kind === 'pods' && pod) {
    if (pod.containers.some((container) => container.state === 'CrashLoopBackOff')) pick = 'crashloop'
    else if (pod.ready < pod.total) pick = 'not-ready'
    else if (pod.restarts > 0) pick = 'restarts'
  } else if (kind === 'deployments') {
    if (failing('Progressing')) pick = 'rollout-stuck'
    else if (failing('Available')) pick = 'unavailable'
  } else if (kind === 'jobs' && conditions.some((c) => c.type === 'Failed' && c.status === 'True')) {
    pick = 'failed'
  }
  if (pick && has(pick)) return pick
  return available[0]?.key
}

/** How an alert is toned: muted is idle, critical is bad, the rest warn. */
export function alertTone(alert: FiringAlert): Tone {
  if (alert.state !== 'firing') return 'idle'
  return severityTone(alert.severity ?? '')
}

/**
 * A firing alert is labelled by its severity when it has a meaningful one.
 * `none` is what kube-prometheus gives Watchdog — an alert that fires to prove
 * the pipeline works — and reads as "Firing", not as a severity called None.
 */
export function alertStateLabel(alert: FiringAlert): string {
  if (alert.state === 'silenced') return 'Silenced'
  if (alert.state === 'inhibited') return 'Inhibited'
  const severity = alert.severity ?? ''
  if (!severity || severity === 'none') return 'Firing'
  return severity[0].toUpperCase() + severity.slice(1)
}

/** The alert's one line: its summary when it has one, else its name. */
export function alertSummary(alert: FiringAlert): string {
  return alert.annotations.summary || alert.annotations.description || alert.name
}

export function severityTone(severity: string): Tone {
  if (severity === 'critical') return 'bad'
  if (severity === 'warning') return 'warn'
  return 'idle'
}

/** An alarm in one line: "Pods restarting above 3, for 5 minutes". */
export function alarmSentence(alarm: Alarm): string {
  const threshold =
    alarm.threshold !== undefined ? ` above ${alarm.threshold}${alarm.threshold_unit ?? ''}` : ''
  return `${alarm.condition_label}${threshold}, ${forWords(alarm.for)}`
}

/** The label an alert names its object by, in kube-state-metrics' vocabulary. */
const OBJECT_LABELS: { label: string; kind: string; title: string; kubemgKind: string }[] = [
  { label: 'deployment', kind: 'deployments', title: 'Deployment', kubemgKind: 'Deployment' },
  { label: 'statefulset', kind: 'statefulsets', title: 'StatefulSet', kubemgKind: 'StatefulSet' },
  { label: 'daemonset', kind: 'daemonsets', title: 'DaemonSet', kubemgKind: 'DaemonSet' },
  { label: 'cronjob', kind: 'cronjobs', title: 'CronJob', kubemgKind: 'CronJob' },
  { label: 'job_name', kind: 'jobs', title: 'Job', kubemgKind: 'Job' },
  { label: 'persistentvolumeclaim', kind: 'persistentvolumeclaims', title: 'PVC', kubemgKind: 'PersistentVolumeClaim' },
  { label: 'pod', kind: 'pods', title: 'Pod', kubemgKind: 'Pod' },
]

/**
 * The object an alert is about: a kubemg alarm names it outright; anything
 * else is read from the label kube-state-metrics names it by, most specific
 * first, so a pod alert raised under a Deployment reads as the Deployment's
 * when the rule carried both.
 */
export function alertObject(alert: FiringAlert): { kind: string; title: string; name: string } | null {
  if (alert.labels.kubemg_kind && alert.labels.kubemg_name) {
    const entry = OBJECT_LABELS.find((candidate) => candidate.kubemgKind === alert.labels.kubemg_kind)
    if (entry) return { kind: entry.kind, title: entry.title, name: alert.labels.kubemg_name }
  }
  for (const entry of OBJECT_LABELS) {
    const name = alert.labels[entry.label]
    if (name) return { kind: entry.kind, title: entry.title, name }
  }
  return null
}

/** The short name a kind is written with beside an object's name. */
export function kindTitle(kind: string): string {
  return OBJECT_LABELS.find((entry) => entry.kind === kind)?.title ?? kind
}
