import { describe, expect, it } from 'vitest'

import { completeGitopsRecord, completeGitopsState } from '../api/gitops'
import {
  autoHealSentence,
  driftedResources,
  healthTone,
  hiddenNote,
  perObjectNote,
  resourceKeyFor,
  sourceLine,
  syncTone,
} from './gitops'
import type { ResourceCategory } from './resources'

const argo = completeGitopsRecord({
  tool: 'argocd',
  namespace: 'argocd',
  name: 'shop',
  sources: [{ repo: 'https://git.example.com/shop.git', path: 'overlays/prod', target_revision: 'main' }],
  sync: 'out_of_sync',
  health: 'healthy',
  auto_heal: false,
  resources: [
    { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'web', sync: 'out_of_sync' },
    { group: '', kind: 'Service', namespace: 'shop', name: 'web', sync: 'synced' },
  ],
})

describe('tones', () => {
  it('reads unknown as idle, never as settled', () => {
    expect(syncTone('unknown')).toBe('idle')
    expect(syncTone('out_of_sync')).toBe('warn')
    expect(syncTone('synced')).toBe('ok')
  })

  it('reserves rust for a broken or missing workload', () => {
    expect(healthTone('degraded')).toBe('bad')
    expect(healthTone('missing')).toBe('bad')
    expect(healthTone('progressing')).toBe('idle')
  })
})

describe('sourceLine', () => {
  it('names repository, path and revision', () => {
    expect(sourceLine(argo)).toBe('https://git.example.com/shop.git · overlays/prod @ main')
  })

  it('counts the sources it does not show', () => {
    const multi = completeGitopsRecord({
      ...argo,
      sources: [{ repo: 'https://a', path: 'x' }, { repo: 'https://b', chart: 'redis' }],
    })
    expect(sourceLine(multi)).toBe('https://a · x (+1 more)')
  })
})

describe('autoHealSentence', () => {
  it('says what self-heal does to a hand edit', () => {
    expect(autoHealSentence(argo)).toMatch(/stays until the next sync/)
    expect(autoHealSentence({ ...argo, auto_heal: true })).toMatch(/puts a hand edit back/)
    expect(autoHealSentence({ ...argo, auto_heal: null })).toMatch(/did not say/)
  })

  it('names a suspended Kustomization as not correcting', () => {
    const ks = completeGitopsRecord({ tool: 'flux-kustomization', suspended: true, auto_heal: true })
    expect(autoHealSentence(ks)).toMatch(/Suspended/)
  })
})

describe('per-object state', () => {
  it('explains Flux and stays quiet for Argo CD', () => {
    expect(perObjectNote('flux-kustomization')).toMatch(/corrects drift rather than recording it/)
    expect(perObjectNote('argocd')).toBeNull()
  })

  it('lists only the drifted entries', () => {
    expect(driftedResources(argo).map((r) => r.kind)).toEqual(['Deployment'])
  })

  it('counts what a scoped grant cannot see', () => {
    expect(hiddenNote(argo)).toBeNull()
    expect(hiddenNote({ ...argo, resources_hidden: 3 })).toBe('3 objects are outside your grant and not shown.')
  })
})

describe('resourceKeyFor', () => {
  const discovered: ResourceCategory[] = [
    {
      id: 'operator:cert-manager',
      label: 'cert-manager',
      items: [
        {
          key: 'crd:cert-manager.io/v1/certificates',
          label: 'Certificates',
          singular: 'Certificate',
          scope: 'namespaced',
          custom: { group: 'cert-manager.io', version: 'v1', plural: 'certificates', scope: 'namespaced' },
        },
      ],
    },
  ] as ResourceCategory[]

  it('maps built-in kinds by group and Kind', () => {
    expect(resourceKeyFor({ group: 'apps', kind: 'Deployment' })).toBe('deployments')
    expect(resourceKeyFor({ group: '', kind: 'ConfigMap' })).toBe('configmaps')
  })

  it('finds a CRD-served kind in the discovered inventory, never by guessing', () => {
    expect(resourceKeyFor({ group: 'cert-manager.io', kind: 'Certificate' }, discovered)).toBe(
      'crd:cert-manager.io/v1/certificates',
    )
    expect(resourceKeyFor({ group: 'example.com', kind: 'Widget' }, discovered)).toBeNull()
  })

  it('does not take a core-group kind it has no list for', () => {
    expect(resourceKeyFor({ group: '', kind: 'Event' })).toBeNull()
  })
})

describe('completeGitopsState', () => {
  it('fills an empty answer without inventing records', () => {
    const state = completeGitopsState({})
    expect(state.available).toBe(false)
    expect(state.records).toEqual([])
    expect(state.summary).toEqual({ records: 0, out_of_sync: 0, degraded: 0 })
  })
})
