import { describe, expect, it } from 'vitest'
import type { VulnWorkloadSummary } from '../api/types'
import {
  acceptExpiry,
  disclosureAuditLink,
  isKubernetesRow,
  epssText,
  filterVulnRows,
  kevText,
  vulnBadgeTitle,
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
    fixable_critical: 0, fixable_high: 0, kev: 0, kev_ids: [], accepted: 0, exposed: false, level: 'none', images: [],
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

describe('what comes first', () => {
  it('names a known-exploited finding before anything else on the badge', () => {
    const exploited = row({ level: 'danger', kev: 1, kev_ids: ['CVE-2024-3094'], fixable_critical: 2, exposed: true })
    expect(vulnBadgeText(exploited)).toBe('1 known exploited')
    expect(vulnBadgeTitle(exploited)).toContain("CVE-2024-3094 on CISA's known-exploited catalogue")
    expect(vulnBadgeTitle(row({ accepted: 2 }))).toContain('2 accepted, not counted')
  })

  it('reads EPSS as a probability and a rank', () => {
    expect(epssText(0.853, 0.996)).toBe('EPSS 85%, top 0.4%')
    expect(epssText(0.034, 0.9)).toBe('EPSS 3.4%, top 10%')
    expect(epssText(0.5)).toBe('EPSS 50%')
  })

  it('says ransomware use on the KEV hover', () => {
    expect(kevText({ ransomware: true, due: '2024-04-22T00:00:00Z' })).toContain('known to be used in ransomware campaigns')
    expect(kevText({ ransomware: false })).not.toContain('ransomware')
  })

  it('sends no expiry for "never" and a date otherwise', () => {
    const now = new Date('2026-01-01T00:00:00Z')
    expect(acceptExpiry(0, now)).toBeUndefined()
    expect(acceptExpiry(30, now)).toBe('2026-01-31T00:00:00.000Z')
  })
})

describe('access since disclosure', () => {
  it('links the namespace, the name and the publish date, in the trail\'s local form', () => {
    const published = new Date(2024, 2, 29, 17, 15).toISOString()
    const link = disclosureAuditLink(4, 'shop', 'web', published)!
    const url = new URL(link, 'https://kubemg.example')
    expect(url.pathname).toBe('/clusters/4/audit')
    expect(url.searchParams.get('namespace')).toBe('shop')
    expect(url.searchParams.get('q')).toBe('web')
    expect(url.searchParams.get('from')).toBe('2024-03-29T17:15')
  })

  it('offers nothing for a cluster-wide row or a finding with no date', () => {
    expect(disclosureAuditLink(4, '', 'k8s-cluster', '2024-03-29T00:00:00Z')).toBeNull()
    expect(disclosureAuditLink(4, 'shop', 'web', undefined)).toBeNull()
    expect(disclosureAuditLink(4, 'shop', 'web', 'not a date')).toBeNull()
  })

  it('knows the Kubernetes row', () => {
    expect(isKubernetesRow(row({ kind: 'Kubernetes', namespace: '' }))).toBe(true)
    expect(isKubernetesRow(row({}))).toBe(false)
  })
})
