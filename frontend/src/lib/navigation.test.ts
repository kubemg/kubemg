import { describe, expect, it } from 'vitest'

import type { Cluster } from '../api/types'
import {
  clusterSlotHref,
  currentClusterSlot,
  namespaceHref,
  resourceHref,
} from './navigation'

/*
 * The cluster address space is a splat with a handful of names carved out of
 * it, which makes "which slot is this" the one derivation here worth pinning:
 * a name that is not carved out is read as a kind, and a kind that does not
 * exist is a page that does not open.
 */

function cluster(over: Partial<Cluster> = {}): Cluster {
  return {
    id: 2,
    name: 'prod',
    environment: 'prod',
    connection_mode: 'agent',
    agent_attached: true,
    namespaces: [],
    ...over,
  } as Cluster
}

describe('currentClusterSlot', () => {
  it('reads a namespace page as a namespace, not as a kind called namespaces/x', () => {
    expect(currentClusterSlot('/clusters/2/namespaces/payments', 2)).toEqual({
      kind: 'namespace',
      name: 'payments',
    })
    // The list itself is still a resource — the carve-out is one segment deep.
    expect(currentClusterSlot('/clusters/2/namespaces', 2)).toEqual({
      kind: 'resource',
      key: 'namespaces',
    })
  })

  it('round-trips a name the address had to escape', () => {
    const href = namespaceHref(2, 'team a/b')
    expect(currentClusterSlot(href, 2)).toEqual({ kind: 'namespace', name: 'team a/b' })
  })

  it('leaves every other address exactly as it read it before', () => {
    expect(currentClusterSlot('/clusters/2/dashboard', 2)).toEqual({
      kind: 'page',
      page: 'dashboard',
    })
    expect(currentClusterSlot('/clusters/2/pods', 2)).toEqual({ kind: 'resource', key: 'pods' })
    // A CRD key carries slashes of its own, which is why the resource slot is
    // the whole tail rather than one segment.
    expect(currentClusterSlot('/clusters/2/crd:acme.io/v1/widgets', 2)).toEqual({
      kind: 'resource',
      key: 'crd:acme.io/v1/widgets',
    })
    expect(currentClusterSlot('/clusters/2', 2)).toEqual({ kind: 'page', page: 'dashboard' })
  })
})

describe('clusterSlotHref', () => {
  it('keeps the question rather than the answer when switching clusters', () => {
    const slot = currentClusterSlot(namespaceHref(2, 'payments'), 2)
    const target = cluster({ id: 9 })

    // `payments` on another cluster is a different namespace or none at all, so
    // carrying the name across would land on a page about nothing.
    expect(clusterSlotHref(target, slot)).toBe(resourceHref(9, 'namespaces'))
    // And a cluster with no tunnel has no namespace list to land on either.
    expect(clusterSlotHref(cluster({ id: 9, agent_attached: false }), slot)).toBe(
      '/clusters/9/dashboard',
    )
  })
})
