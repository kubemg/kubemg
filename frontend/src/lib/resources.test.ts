import { describe, expect, it } from 'vitest'

import {
  discoverCategories,
  exploreCategories,
  isOperatorCategory,
  matchesResource,
  RESOURCE_CATEGORIES,
  resourceItem,
  resourceSingular,
} from './resources'

/*
 * The sidebar a cluster gets is derived from that cluster's own CRDs, and the
 * derivation is the part with rules in it: which family earns a section, what a
 * section is called, and where a discovered section sits relative to the fixed
 * inventory. None of that needs a cluster to assert — it needs a CRD list.
 */

const crd = (group: string, kind: string, plural: string, over: Partial<Crd> = {}): Crd => ({
  group,
  kind,
  plural,
  scope: 'Namespaced',
  versions: ['v1'],
  ...over,
})

interface Crd {
  group: string
  kind: string
  plural: string
  scope: string
  versions: string[]
}

describe('discoverCategories', () => {
  it('gives a family with two kinds its own section, and a singleton Other', () => {
    const sections = discoverCategories([
      crd('kafka.strimzi.io', 'Kafka', 'kafkas'),
      crd('core.strimzi.io', 'StrimziPodSet', 'strimzipodsets'),
      crd('acme.example.com', 'Widget', 'widgets'),
    ])

    expect(sections.map((section) => [section.id, section.label])).toEqual([
      ['operator:strimzi.io', 'Strimzi'],
      ['other', 'Other'],
    ])
    // One operator, two groups, one section: the domain root is what says
    // "these are one area of work".
    expect(sections[0].items.map((item) => item.label)).toEqual(['Kafkas', 'StrimziPodSets'])
    expect(sections[1].items.map((item) => item.label)).toEqual(['Widgets'])
    expect(isOperatorCategory(sections[0].id)).toBe(true)
  })

  it('keeps one more label under a domain root that belongs to nobody', () => {
    // Grouping by `k8s.io` would put half the ecosystem in a section named
    // after a registrar.
    const sections = discoverCategories([
      crd('cluster.x-k8s.io', 'Cluster', 'clusters'),
      crd('bootstrap.cluster.x-k8s.io', 'KubeadmConfig', 'kubeadmconfigs'),
    ])
    expect(sections.map((section) => section.id)).toEqual(['operator:cluster.x-k8s.io'])
  })

  it('puts the Gateway API in Networking rather than in a section of its own', () => {
    const sections = discoverCategories([
      crd('gateway.networking.k8s.io', 'HTTPRoute', 'httproutes'),
      crd('gateway.networking.k8s.io', 'Gateway', 'gateways'),
    ])
    expect(sections.map((section) => section.id)).toEqual(['networking'])
    // And the one it reads first-class keeps its own entry rather than the
    // generic derived one.
    const route = sections[0].items.find((item) => item.key === 'httproutes')
    expect(route?.label).toBe('HTTPRoutes')
  })

  it('names a section from its own domain when the derived name is taken', () => {
    // "Cluster" is a fixed section, and two sections with one name is worse
    // than one named after its domain.
    const sections = discoverCategories([
      crd('a.cluster.io', 'Alpha', 'alphas'),
      crd('b.cluster.io', 'Beta', 'betas'),
    ])
    expect(sections[0].label).toBe('cluster.io')
  })

  it('pluralises a Kind for the heading the way English does', () => {
    const sections = discoverCategories([
      crd('acme.example.com', 'NetworkPolicy', 'networkpolicies'),
      crd('acme.example.com', 'ServiceEntry', 'serviceentries'),
      crd('acme.example.com', 'Mesh', 'meshes'),
    ])
    expect(sections[0].items.map((item) => item.label).sort()).toEqual([
      'Meshes',
      'NetworkPolicies',
      'ServiceEntries',
    ])
  })

  it('reads the newest stable version, and skips a CRD serving none', () => {
    const sections = discoverCategories([
      crd('acme.example.com', 'Widget', 'widgets', { versions: ['v1beta1', 'v1', 'v2'] }),
      crd('acme.example.com', 'Gadget', 'gadgets', { versions: ['v1alpha1', 'v1beta1'] }),
      crd('acme.example.com', 'Ghost', 'ghosts', { versions: [] }),
    ])
    const keys = sections[0].items.map((item) => item.key)
    expect(keys).toContain('crd:acme.example.com/v2/widgets')
    expect(keys).toContain('crd:acme.example.com/v1beta1/gadgets')
    expect(keys.length).toBe(2)
  })

  it('carries the group so two same-named kinds can be told apart', () => {
    const sections = discoverCategories([
      crd('acme.example.com', 'Widget', 'widgets'),
      crd('acme.example.com', 'Gadget', 'gadgets'),
    ])
    const widget = sections[0].items[1]
    expect(widget.label).toBe('Widgets')
    expect(matchesResource(widget, 'acme.example.com')).toBe(true)
  })
})

describe('exploreCategories', () => {
  it('extends a fixed section rather than repeating it', () => {
    const merged = exploreCategories(
      discoverCategories([
        crd('gateway.networking.k8s.io', 'HTTPRoute', 'httproutes'),
        crd('gateway.networking.k8s.io', 'Gateway', 'gateways'),
      ]),
    )
    expect(merged.filter((section) => section.id === 'networking').length).toBe(1)
    const networking = merged.find((section) => section.id === 'networking')
    expect(networking?.items.some((item) => item.key === 'services')).toBe(true)
    expect(networking?.items.some((item) => item.key === 'httproutes')).toBe(true)
    // The discovered kinds go after the fixed ones, not among them.
    const keys = networking?.items.map((item) => item.key) ?? []
    expect(keys.indexOf('services')).toBeLessThan(keys.indexOf('httproutes'))
  })

  it('puts a discovered section below the whole fixed inventory, Other last', () => {
    const merged = exploreCategories(
      discoverCategories([
        crd('kafka.strimzi.io', 'Kafka', 'kafkas'),
        crd('core.strimzi.io', 'StrimziPodSet', 'strimzipodsets'),
        crd('acme.example.com', 'Widget', 'widgets'),
      ]),
    )
    const ids = merged.map((section) => section.id)
    expect(ids.slice(0, RESOURCE_CATEGORIES.length)).toEqual(
      RESOURCE_CATEGORIES.map((section) => section.id),
    )
    expect(ids.slice(RESOURCE_CATEGORIES.length)).toEqual(['operator:strimzi.io', 'other'])
  })

  it('is the fixed inventory unchanged when a cluster has nothing extra', () => {
    expect(exploreCategories([])).toEqual(RESOURCE_CATEGORIES)
  })
})

describe('resourceItem', () => {
  it('resolves a fixed key anywhere, and a crd: key only where that CRD is served', () => {
    expect(resourceItem('pods')?.label).toBe('Pods')
    const discovered = discoverCategories([
      crd('acme.example.com', 'Widget', 'widgets'),
      crd('acme.example.com', 'Gadget', 'gadgets'),
    ])
    const key = 'crd:acme.example.com/v1/widgets' as const
    expect(resourceItem(key)).toBeNull()
    expect(resourceItem(key, discovered)?.label).toBe('Widgets')
  })
})

describe('matchesResource and resourceSingular', () => {
  it('matches a label, a key or a short name', () => {
    const pods = resourceItem('pods')
    if (!pods) throw new Error('pods is a fixed entry')
    expect(matchesResource(pods, '')).toBe(true)
    expect(matchesResource(pods, 'POD')).toBe(true)
    expect(matchesResource(pods, 'ingress')).toBe(false)

    const claims = resourceItem('persistentvolumeclaims')
    if (!claims) throw new Error('persistentvolumeclaims is a fixed entry')
    expect(matchesResource(claims, 'pvc')).toBe(true)
  })

  it('names one object of a kind, trusting the declared singular where there is one', () => {
    const pods = resourceItem('pods')
    const ingresses = resourceItem('ingresses')
    if (!pods || !ingresses) throw new Error('both are fixed entries')
    expect(resourceSingular(pods)).toBe('Pod')
    // "Ingresse" is what dropping the s would give.
    expect(resourceSingular(ingresses)).toBe('Ingress')
  })
})
