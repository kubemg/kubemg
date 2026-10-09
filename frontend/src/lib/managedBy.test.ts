import { describe, expect, it } from 'vitest'

import type { ManagedBy } from '../api/types'
import { argoApplicationHref } from './consoles'
import { managedByLabel, managedNotice, managedSelectionNotice, managedTone } from './managedBy'

const operator: ManagedBy = { manager: 'controller', kind: 'PostgresCluster', name: 'orders', reverts: true }
const argo: ManagedBy = { manager: 'argocd', name: 'shop', reverts: false }
const argoElsewhere: ManagedBy = { manager: 'argocd', name: 'shop', namespace: 'team-a', reverts: false }
const kustomization: ManagedBy = {
  manager: 'flux',
  kind: 'Kustomization',
  name: 'apps',
  namespace: 'flux-system',
  reverts: true,
}
const kustomizationOff: ManagedBy = { manager: 'flux', kind: 'Kustomization', name: 'apps', reverts: false }
const helmRelease: ManagedBy = { manager: 'flux', kind: 'HelmRelease', name: 'redis', reverts: false }
const helm: ManagedBy = { manager: 'helm', name: 'redis', namespace: 'cache', reverts: false }

describe('managedNotice', () => {
  it('says nothing about an object nothing manages', () => {
    expect(managedNotice(undefined, 'Deployment', 'change')).toBeNull()
  })

  it('names the manager and where a lasting change belongs', () => {
    expect(managedNotice(operator, 'StatefulSet', 'change')).toBe(
      'This StatefulSet is controlled by PostgresCluster orders. The PostgresCluster rewrites it ' +
        'from its own spec, so a change made here will be undone. A lasting change belongs on the ' +
        'PostgresCluster.',
    )
    expect(managedNotice(argo, 'Deployment', 'change')).toMatch(
      /^This Deployment is deployed by the Argo CD application shop\. .*belongs in Git\.$/,
    )
    expect(managedNotice(kustomization, 'Deployment', 'change')).toMatch(
      /Flux Kustomization flux-system\/apps\. Flux puts back every field/,
    )
    expect(managedNotice(helmRelease, 'Deployment', 'change')).toMatch(/installed by the Flux HelmRelease redis/)
    expect(managedNotice(helm, 'Deployment', 'change')).toMatch(
      /belongs to the Helm release cache\/redis\. The release’s next upgrade renders over/,
    )
  })

  it('does not claim a revert Argo CD might not make', () => {
    // Self-heal is a setting on the Application, which is never read.
    expect(managedNotice(argo, 'Deployment', 'change')).toMatch(/If the application self-heals/)
    expect(managedNotice(argo, 'Deployment', 'change')).not.toMatch(/will be undone/)
  })

  it('honours Flux’s off switch rather than warning about a revert that will not come', () => {
    expect(managedNotice(kustomizationOff, 'Deployment', 'change')).toMatch(/stays until/)
    expect(managedNotice(kustomizationOff, 'Deployment', 'delete')).toMatch(/will not recreate it/)
  })

  it('words a delete as a recreate', () => {
    expect(managedNotice(operator, 'StatefulSet', 'delete')).toMatch(/not final: the PostgresCluster recreates it/)
    expect(managedNotice(kustomization, 'Deployment', 'delete')).toMatch(/Flux recreates it/)
    expect(managedNotice(helm, 'Deployment', 'delete')).toMatch(/next upgrade recreates it/)
  })

  it('leaves a restart alone unless a controller owns the pod template', () => {
    // The restart annotation is a field no manifest in Git sets.
    expect(managedNotice(argo, 'Deployment', 'restart')).toBeNull()
    expect(managedNotice(kustomization, 'Deployment', 'restart')).toBeNull()
    expect(managedNotice(helm, 'Deployment', 'restart')).toBeNull()
    expect(managedNotice(operator, 'StatefulSet', 'restart')).toMatch(/rolls the pods a second time/)
  })
})

describe('managedTone', () => {
  it('warns only where the revert is certain', () => {
    expect(managedTone(operator)).toBe('warn')
    expect(managedTone(kustomization)).toBe('warn')
    expect(managedTone(argo)).toBe('info')
    expect(managedTone(helm)).toBe('info')
  })
})

describe('managedByLabel', () => {
  it('qualifies the managing object by its namespace when known', () => {
    expect(managedByLabel(argoElsewhere)).toBe('Argo CD application team-a/shop')
    expect(managedByLabel(operator)).toBe('PostgresCluster orders')
  })
})

describe('managedSelectionNotice', () => {
  it('is absent when nothing selected is managed', () => {
    expect(managedSelectionNotice([{ label: 'Pod' }, { label: 'Pod' }], 'delete')).toBeNull()
  })

  it('is the object’s own sentence for a one-row selection', () => {
    expect(managedSelectionNotice([{ label: 'StatefulSet', managedBy: operator }], 'delete')).toEqual({
      text: managedNotice(operator, 'StatefulSet', 'delete'),
      tone: 'warn',
    })
  })

  it('counts the managed rows of a larger selection', () => {
    const rows = [
      { label: 'Deployment', managedBy: argo },
      { label: 'Deployment' },
      { label: 'Deployment', managedBy: helm },
    ]
    expect(managedSelectionNotice(rows, 'delete')).toEqual({
      text: '2 of the 3 selected may be recreated by what manages them. Each one is marked below with what manages it.',
      tone: 'info',
    })
  })

  it('counts only rows the act runs into', () => {
    // A restart under Argo CD is not one of them.
    const rows = [
      { label: 'Deployment', managedBy: argo },
      { label: 'StatefulSet', managedBy: operator },
    ]
    expect(managedSelectionNotice(rows, 'restart')?.text).toMatch(/^1 of the 2 selected is controlled/)
    expect(managedSelectionNotice([{ label: 'Deployment', managedBy: argo }], 'restart')).toBeNull()
  })
})

describe('argoApplicationHref', () => {
  it('addresses an application outside Argo CD’s namespace by both names', () => {
    expect(argoApplicationHref('https://argo.example.com/', 'shop')).toBe(
      'https://argo.example.com/applications/shop',
    )
    expect(argoApplicationHref('https://argo.example.com', 'shop', 'team-a')).toBe(
      'https://argo.example.com/applications/team-a/shop',
    )
  })
})
