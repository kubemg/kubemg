import { describe, expect, it } from 'vitest'
import type { VulnWorkloadSummary } from '../api/types'
import {
  filterVulnRows,
  indexVulnSummaries,
  mayAskForVulns,
  vulnBadgeText,
  vulnKey,
  vulnNavCount,
  vulnRailDot,
  vulnTone,
} from './vulnFindings'

function row(over: Partial<VulnWorkloadSummary>): VulnWorkloadSummary {
  return {
    namespace: 'shop', kind: 'Deployment', name: 'web',
    critical: 0, high: 0, medium: 0, low: 0, unknown: 0,
    fixable_critical: 0, fixable_high: 0, exposed: false, level: 'none', images: [],
    ...over,
  }
}

describe('who is asked', () => {
  it('asks only for an unscoped cluster-admin grant on an agent cluster', () => {
    expect(mayAskForVulns({ k8s_role: 'cluster-admin', namespaces: [], connection_mode: 'agent' })).toBe(true)
    expect(mayAskForVulns({ k8s_role: 'cluster-admin', namespaces: ['shop'], connection_mode: 'agent' })).toBe(false)
    expect(mayAskForVulns({ k8s_role: 'edit', namespaces: [], connection_mode: 'agent' })).toBe(false)
    expect(mayAskForVulns({ k8s_role: 'cluster-admin', namespaces: [], connection_mode: 'direct' })).toBe(false)
  })
})

describe('colour', () => {
  it('spends rust on danger only and nothing on none', () => {
    expect(vulnTone('danger')).toBe('bad')
    expect(vulnTone('warn')).toBe('warn')
    expect(vulnTone('none')).toBeNull()
  })

  it('says why a badge is coloured', () => {
    expect(vulnBadgeText(row({ level: 'danger', fixable_critical: 2, exposed: true }))).toBe('2 fixable critical, exposed')
    expect(vulnBadgeText(row({ level: 'warn', fixable_high: 3 }))).toBe('3 fixable high')
    expect(vulnBadgeText(row({ level: 'warn', fixable_critical: 1, fixable_high: 1 }))).toBe('2 fixable critical/high')
  })
})

describe('the sidebar', () => {
  it('counts danger and warn, and is silent at zero', () => {
    expect(vulnNavCount({ cluster_id: 1, danger: 1, warn: 2 })).toBe(3)
    expect(vulnNavCount({ cluster_id: 1, danger: 0, warn: 0 })).toBeNull()
    expect(vulnNavCount(undefined)).toBeNull()
  })

  it('marks the rail for danger only', () => {
    expect(vulnRailDot({ cluster_id: 1, danger: 0, warn: 9 })).toBe(false)
    expect(vulnRailDot({ cluster_id: 1, danger: 1, warn: 0 })).toBe(true)
  })
})

describe('rows', () => {
  const rows = [
    row({ name: 'web', level: 'danger', images: ['nginx:1.0'] }),
    row({ name: 'db', namespace: 'data', kind: 'StatefulSet', level: 'warn' }),
    row({ name: 'batch', level: 'none' }),
  ]

  it('finds a row by kind, namespace and name', () => {
    expect(indexVulnSummaries(rows).get(vulnKey('StatefulSet', 'data', 'db'))?.level).toBe('warn')
  })

  it('shows only what needs attention by default, and searches images', () => {
    expect(filterVulnRows(rows, 'attention', '').map((r) => r.name)).toEqual(['web', 'db'])
    expect(filterVulnRows(rows, 'all', '').length).toBe(3)
    expect(filterVulnRows(rows, 'all', 'nginx').map((r) => r.name)).toEqual(['web'])
  })
})
