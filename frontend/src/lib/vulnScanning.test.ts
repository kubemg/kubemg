import { describe, expect, it } from 'vitest'
import type { VulnDetection, VulnRecommendation, VulnScanning } from '../api/types'
import { operatorState, reportKindLabel, vulnNextStep, vulnTileState } from './vulnScanning'

const recommendation: VulnRecommendation = {
  provider: 'trivy-operator',
  name: 'Trivy Operator',
  license: 'Apache-2.0',
  repository_name: 'aqua',
  repository_url: 'https://aquasecurity.github.io/helm-charts/',
  chart: 'trivy-operator',
  release: 'trivy-operator',
  namespace: 'trivy-system',
  docs_url: 'https://aquasecurity.github.io/trivy-operator/latest/',
  catalogue: null,
}

const scanning: VulnScanning = {
  cluster_id: 1,
  provider: 'trivy-operator',
  enabled: true,
  last_status: 'healthy',
  created_at: '2026-11-16T00:00:00Z',
  updated_at: '2026-11-16T00:00:00Z',
}

const detection = (patch: Partial<VulnDetection> = {}): VulnDetection => ({
  provider: 'trivy-operator',
  installed: false,
  reports: [],
  operator: null,
  status: 'unhealthy',
  message: '',
  ...patch,
})

describe('vulnNextStep', () => {
  it('walks add → sync → install → connect → connected', () => {
    expect(vulnNextStep(null, detection(), recommendation)).toBe('add-repository')
    expect(
      vulnNextStep(null, detection(), { ...recommendation, catalogue: { repository: 'aqua', status: 'pending' } }),
    ).toBe('sync-repository')
    expect(
      vulnNextStep(null, detection(), {
        ...recommendation,
        catalogue: { repository: 'aqua', status: 'ok', chart_version: '0.29.0' },
      }),
    ).toBe('install')
    expect(vulnNextStep(null, detection({ installed: true }), recommendation)).toBe('connect')
    expect(vulnNextStep(scanning, detection({ installed: true }), recommendation)).toBe('connected')
  })

  it('offers nothing to install before the cluster has been looked at', () => {
    // A detection that has not answered yet is not "not installed": the sheet
    // must not tell an administrator to install something already there.
    expect(vulnNextStep(null, null, recommendation)).toBe('add-repository')
  })
})

describe('vulnTileState', () => {
  it('reads a stored decision, then its health', () => {
    expect(vulnTileState(null)).toEqual({ tone: 'idle', label: 'Not connected' })
    expect(vulnTileState({ ...scanning, enabled: false })).toEqual({ tone: 'idle', label: 'Paused' })
    expect(vulnTileState(scanning).tone).toBe('ok')
    expect(vulnTileState({ ...scanning, last_status: 'unhealthy' }).tone).toBe('bad')
    expect(vulnTileState({ ...scanning, last_status: 'pending' }).label).toBe('Never checked')
  })
})

describe('reportKindLabel', () => {
  const base = { kind: 'vulnerabilityreports', label: 'Workload images', covers: 'workloads' as const, required: true }
  it('never says 0 for a kind the cluster does not serve', () => {
    expect(reportKindLabel({ ...base, served: false }).text).toBe('Not served by this cluster')
    expect(reportKindLabel({ ...base, served: true, count: 0 }).text).toBe('0 reports')
    expect(reportKindLabel({ ...base, served: true, count: 1 }).text).toBe('1 report')
    expect(reportKindLabel({ ...base, served: true, count: 42, approximate: true }).text).toBe('about 42 reports')
    expect(reportKindLabel({ ...base, served: true }).text).toBe('Served')
    expect(reportKindLabel({ ...base, served: true, reason: 'forbidden' }).tone).toBe('bad')
  })
})

describe('operatorState', () => {
  const operator = { namespace: 'trivy-system', name: 'trivy-operator', replicas: 1, ready_replicas: 1, scanner_disabled: false }
  it('tells a scanner that is not scanning from one that is', () => {
    expect(operatorState(detection({ operator })).tone).toBe('ok')
    expect(operatorState(detection({ operator: { ...operator, ready_replicas: 0 } })).text).toBe('Not running')
    expect(operatorState(detection({ operator: { ...operator, scanner_disabled: true } })).tone).toBe('bad')
    expect(operatorState(detection()).tone).toBe('idle')
    expect(operatorState(detection({ operator_reason: 'forbidden' })).tone).toBe('warn')
  })
})
