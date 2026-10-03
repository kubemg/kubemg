import { describe, expect, it } from 'vitest'

import type { AlarmCondition, FiringAlert, ObservabilitySource, Pod, PodContainer } from '../api/types'
import {
  alarmSentence,
  alertObject,
  alertStateLabel,
  alertTone,
  forWords,
  hasAlerting,
  suggestCondition,
  supportsAlarms,
} from './alerting'

function condition(key: string): AlarmCondition {
  return { key, label: key, description: '', default_for: '5m', default_severity: 'warning' }
}

function alert(over: Partial<FiringAlert> = {}): FiringAlert {
  return {
    fingerprint: 'f',
    name: 'X',
    state: 'firing',
    starts_at: '2026-10-01T10:00:00Z',
    labels: {},
    annotations: {},
    kubemg: false,
    can_silence: false,
    ...over,
  }
}

function pod(over: Partial<Pod>, state = 'running'): Pod {
  const container: PodContainer = {
    name: 'app',
    image: 'app:1',
    ready: true,
    restarts: 0,
    state,
    cpu_request_millicores: 0,
    cpu_limit_millicores: 0,
    memory_request_bytes: 0,
    memory_limit_bytes: 0,
  }
  return {
    name: 'p',
    namespace: 'shop',
    phase: 'Running',
    node: 'n',
    ready: 1,
    total: 1,
    restarts: 0,
    created_at: '2026-01-01T00:00:00Z',
    containers: [container],
    ephemeral_containers: [],
    ...over,
  }
}

describe('suggestCondition', () => {
  const podConditions = [condition('not-ready'), condition('crashloop'), condition('restarts')]

  it('opens on what is wrong with the object now', () => {
    expect(suggestCondition('pods', podConditions, [], pod({}, 'CrashLoopBackOff'))).toBe('crashloop')
    expect(suggestCondition('pods', podConditions, [], pod({ ready: 0 }))).toBe('not-ready')
    expect(suggestCondition('pods', podConditions, [], pod({ restarts: 4 }))).toBe('restarts')

    const deployment = [condition('unavailable'), condition('rollout-stuck'), condition('restarts')]
    expect(suggestCondition('deployments', deployment, [{ type: 'Progressing', status: 'False' }])).toBe(
      'rollout-stuck',
    )
    expect(suggestCondition('deployments', deployment, [{ type: 'Available', status: 'False' }])).toBe(
      'unavailable',
    )
  })

  it('falls back to the first condition when nothing is wrong', () => {
    expect(suggestCondition('pods', podConditions, [], pod({}))).toBe('not-ready')
    expect(suggestCondition('pods', [], [], pod({}))).toBeUndefined()
  })
})

describe('hasAlerting', () => {
  it('offers alarms only with an Alertmanager registered and switched on', () => {
    const source = (kind: string, enabled: boolean) => ({ kind, enabled }) as ObservabilitySource
    expect(hasAlerting(undefined)).toBe(false)
    expect(hasAlerting([source('metrics', true)])).toBe(false)
    expect(hasAlerting([source('alerts', false)])).toBe(false)
    expect(hasAlerting([source('metrics', true), source('alerts', true)])).toBe(true)
  })
})

describe('alertObject', () => {
  it('reads a kubemg alarm by its own labels, else by kube-state-metrics', () => {
    expect(
      alertObject(alert({ labels: { kubemg_kind: 'PersistentVolumeClaim', kubemg_name: 'data', persistentvolumeclaim: 'data' } })),
    ).toEqual({ kind: 'persistentvolumeclaims', title: 'PVC', name: 'data' })
    expect(alertObject(alert({ labels: { deployment: 'web', pod: 'web-1' } }))).toEqual({
      kind: 'deployments',
      title: 'Deployment',
      name: 'web',
    })
    expect(alertObject(alert({ labels: { alertname: 'Watchdog' } }))).toBeNull()
  })
})

describe('wording', () => {
  it('tones muted alerts idle and critical ones bad', () => {
    expect(alertTone(alert({ severity: 'critical' }))).toBe('bad')
    expect(alertTone(alert({ severity: 'warning' }))).toBe('warn')
    expect(alertTone(alert({ severity: 'critical', state: 'silenced' }))).toBe('idle')
    expect(alertTone(alert({ severity: 'none' }))).toBe('idle')
    expect(alertStateLabel(alert({ severity: 'none' }))).toBe('Firing')
    expect(alertStateLabel(alert({ severity: 'critical' }))).toBe('Critical')
  })

  it('says an alarm in one line', () => {
    expect(forWords('0m')).toBe('at once')
    expect(
      alarmSentence({
        namespace: 'shop',
        name: 'r',
        kind: 'persistentvolumeclaims',
        target: 'data',
        condition: 'filling',
        condition_label: 'Volume filling up',
        threshold: 85,
        threshold_unit: '%',
        for: '5m',
        severity: 'warning',
        expr: '',
        created_at: '',
      }),
    ).toBe('Volume filling up above 85%, for 5 minutes')
    expect(supportsAlarms('deployments')).toBe(true)
    expect(supportsAlarms('secrets')).toBe(false)
  })
})
