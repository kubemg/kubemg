import type { ResourceCondition, ResourceDescribeResult, ResourceField } from '../api/types'
import type { Tone } from './status'

/*
 * What a drawer can draw about an object before anybody reads a line of it.
 *
 * Everything here comes off the describe already on screen — the flattened
 * spec and status the server sends for every kind — so a picture costs no
 * second read and cannot disagree with the Describe tab. Three rules:
 *
 *   - **Kubernetes omits a zero.** `readyReplicas` is absent on a Deployment
 *     with nothing ready, so an absent count reads 0 — but only where the
 *     field is a count the controller writes. A desired size the spec leaves
 *     out is the API server's default (1), not 0.
 *   - **A kind this does not know draws nothing.** A CRD with a `replicas`
 *     field is not guessed at; it keeps the facts row and the Describe tab.
 *   - **A list of objects is a count, not a value.** The flatten writes
 *     `2 items` for ports or load-balancer addresses, and that is all that is
 *     read from it.
 */

export interface GlanceBar {
  label: string
  value: number
  of: number
}

export interface GlanceSegment {
  key: string
  label: string
  value: number
  /** A background utility from the deck's tokens. */
  fill: string
}

export interface GlanceChip {
  label: string
  value: string
  tone?: Tone
  /** Set as data (an identifier, a figure) rather than as a phrase. */
  data?: boolean
}

export interface Glance {
  /** A workload's size: one mark per desired replica, `ready` of them lit. */
  replicas?: { desired: number; ready: number; noun: string }
  /** Progress lines against the desired size. */
  bars: GlanceBar[]
  /** A split of one count — a Job's pods by outcome. */
  segments?: { label: string; total: number; parts: GlanceSegment[] }
  chips: GlanceChip[]
}

function field(fields: readonly ResourceField[], path: string): string | undefined {
  return fields.find((entry) => entry.path === path)?.value
}

function count(fields: readonly ResourceField[], path: string, absent: number): number {
  const raw = field(fields, path)
  if (raw === undefined) return absent
  const value = Number(raw)
  return Number.isFinite(value) && value >= 0 ? value : absent
}

/** How many entries a flattened list of objects holds — `2 items` → 2. */
export function itemCount(value: string | undefined): number {
  if (!value) return 0
  const match = /^(\d+) items?$/.exec(value)
  return match ? Number(match[1]) : 0
}

function chip(label: string, value: string | undefined, extra: Partial<GlanceChip> = {}): GlanceChip[] {
  return value ? [{ label, value, data: true, ...extra }] : []
}

const PVC_PHASE_TONE: Record<string, Tone> = { Bound: 'ok', Pending: 'warn', Lost: 'bad' }

export function glanceFor(kind: string, describe: ResourceDescribeResult): Glance | null {
  const spec = describe.spec_summary
  const status = describe.status_summary

  switch (kind) {
    case 'deployments': {
      const desired = count(spec, 'replicas', 1)
      const paused = field(spec, 'paused') === 'true'
      return {
        replicas: { desired, ready: count(status, 'readyReplicas', 0), noun: 'replica' },
        bars: [
          { label: 'Up to date', value: count(status, 'updatedReplicas', 0), of: desired },
          { label: 'Available', value: count(status, 'availableReplicas', 0), of: desired },
        ],
        chips: [
          ...chip('Strategy', field(spec, 'strategy.type')),
          ...chip('Max surge', field(spec, 'strategy.rollingUpdate.maxSurge')),
          ...chip('Max unavailable', field(spec, 'strategy.rollingUpdate.maxUnavailable')),
          ...(paused ? [{ label: 'Rollout', value: 'paused', tone: 'warn' as Tone }] : []),
        ],
      }
    }

    case 'statefulsets': {
      const desired = count(spec, 'replicas', 1)
      return {
        replicas: { desired, ready: count(status, 'readyReplicas', 0), noun: 'replica' },
        bars: [
          { label: 'Up to date', value: count(status, 'updatedReplicas', 0), of: desired },
          { label: 'Available', value: count(status, 'availableReplicas', 0), of: desired },
        ],
        chips: [
          ...chip('Service', field(spec, 'serviceName')),
          ...chip('Update', field(spec, 'updateStrategy.type')),
          ...chip('Pod management', field(spec, 'podManagementPolicy')),
        ],
      }
    }

    case 'daemonsets': {
      const desired = count(status, 'desiredNumberScheduled', 0)
      const misscheduled = count(status, 'numberMisscheduled', 0)
      return {
        replicas: { desired, ready: count(status, 'numberReady', 0), noun: 'node' },
        bars: [
          { label: 'Up to date', value: count(status, 'updatedNumberScheduled', 0), of: desired },
          { label: 'Available', value: count(status, 'numberAvailable', 0), of: desired },
        ],
        chips: [
          ...chip('Update', field(spec, 'updateStrategy.type')),
          ...(misscheduled > 0
            ? [{ label: 'Misscheduled', value: String(misscheduled), tone: 'warn' as Tone, data: true }]
            : []),
        ],
      }
    }

    case 'replicasets': {
      const desired = count(spec, 'replicas', 1)
      return {
        replicas: { desired, ready: count(status, 'readyReplicas', 0), noun: 'replica' },
        bars: [{ label: 'Available', value: count(status, 'availableReplicas', 0), of: desired }],
        chips: [],
      }
    }

    case 'jobs': {
      const completions = count(spec, 'completions', 1)
      const succeeded = count(status, 'succeeded', 0)
      const failed = count(status, 'failed', 0)
      const active = count(status, 'active', 0)
      const pending = Math.max(0, completions - succeeded - active)
      return {
        bars: [],
        segments: {
          label: `${succeeded} of ${completions} completed`,
          total: completions,
          parts: [
            { key: 'succeeded', label: 'Succeeded', value: succeeded, fill: 'bg-ok' },
            // Running is neither good nor bad yet, so it takes a category
            // colour rather than a state one — and never lime, which is
            // reserved for what can be clicked.
            { key: 'active', label: 'Running', value: active, fill: 'bg-chart-1' },
            { key: 'failed', label: 'Failed', value: failed, fill: 'bg-danger' },
            { key: 'pending', label: 'Not started', value: pending, fill: 'bg-line' },
          ],
        },
        chips: [
          ...chip('Parallelism', field(spec, 'parallelism')),
          ...chip('Backoff limit', field(spec, 'backoffLimit')),
          ...(field(spec, 'suspend') === 'true'
            ? [{ label: 'Job', value: 'suspended', tone: 'warn' as Tone }]
            : []),
        ],
      }
    }

    case 'cronjobs': {
      const suspended = field(spec, 'suspend') === 'true'
      return {
        bars: [],
        chips: [
          ...chip('Schedule', field(spec, 'schedule')),
          ...chip('Time zone', field(spec, 'timeZone')),
          ...chip('Concurrency', field(spec, 'concurrencyPolicy')),
          {
            label: 'State',
            value: suspended ? 'suspended' : 'scheduling',
            tone: suspended ? 'warn' : 'ok',
          },
        ],
      }
    }

    case 'services': {
      const ports = itemCount(field(spec, 'ports'))
      const type = field(spec, 'type') ?? 'ClusterIP'
      const addresses = itemCount(field(status, 'loadBalancer.ingress'))
      return {
        bars: [],
        chips: [
          { label: 'Type', value: type, data: true },
          ...chip('Cluster IP', field(spec, 'clusterIP')),
          ...(ports > 0 ? [{ label: 'Ports', value: String(ports), data: true }] : []),
          ...(type === 'LoadBalancer'
            ? [
                addresses > 0
                  ? { label: 'Address', value: 'assigned', tone: 'ok' as Tone }
                  : { label: 'Address', value: 'pending', tone: 'warn' as Tone },
              ]
            : []),
          ...chip('Affinity', field(spec, 'sessionAffinity') === 'None' ? undefined : field(spec, 'sessionAffinity')),
        ],
      }
    }

    case 'ingresses': {
      const addresses = itemCount(field(status, 'loadBalancer.ingress'))
      const rules = itemCount(field(spec, 'rules'))
      const tls = itemCount(field(spec, 'tls'))
      return {
        bars: [],
        chips: [
          ...chip('Class', field(spec, 'ingressClassName')),
          ...(rules > 0 ? [{ label: 'Rules', value: String(rules), data: true }] : []),
          { label: 'TLS', value: tls > 0 ? `${tls} ${tls === 1 ? 'block' : 'blocks'}` : 'none', tone: tls > 0 ? 'ok' : 'idle' },
          addresses > 0
            ? { label: 'Address', value: 'assigned', tone: 'ok' }
            : { label: 'Address', value: 'none yet', tone: 'warn' },
        ],
      }
    }

    case 'persistentvolumeclaims': {
      const phase = field(status, 'phase')
      return {
        bars: [],
        chips: [
          ...(phase ? [{ label: 'Phase', value: phase, tone: PVC_PHASE_TONE[phase] ?? 'idle' }] : []),
          ...chip('Capacity', field(status, 'capacity.storage') ?? field(spec, 'resources.requests.storage')),
          ...chip('Class', field(spec, 'storageClassName')),
          ...chip('Access', field(spec, 'accessModes')),
          ...chip('Volume', field(spec, 'volumeName')),
        ],
      }
    }

    default:
      return null
  }
}

/** The tone a workload's replica count reads in. */
export function replicaTone(ready: number, desired: number): Tone {
  if (desired === 0) return 'idle'
  if (ready >= desired) return 'ok'
  if (ready === 0) return 'bad'
  return 'warn'
}

/**
 * Conditions whose healthy value is False rather than True. Kubernetes has no
 * flag for this — the polarity is a naming convention — so it is matched by
 * name and kept deliberately short: a condition this does not know is treated
 * as positive, which fails towards saying nothing rather than towards crying
 * wolf on every CRD that invents a condition type.
 */
export const NEGATIVE_CONDITIONS = /^(Failed|.*Pressure|NetworkUnavailable|Degraded)$/

export function conditionTone(condition: ResourceCondition): Tone {
  if (condition.status === 'Unknown') return 'warn'
  const negative = NEGATIVE_CONDITIONS.test(condition.type)
  const healthy = negative ? condition.status === 'False' : condition.status === 'True'
  return healthy ? 'ok' : 'bad'
}
