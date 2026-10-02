/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Cluster, NetworkPolicyCoverage } from '../api/types'
import { NetworkPolicyCoveragePanel } from './NetworkPolicyCoveragePanel'

/*
 * What the panel has to get right is which gap it calls a finding: a namespace
 * where some pods are selected and others are not is the sharp one, while a
 * namespace with no policy at all is a quiet fact rather than an alarm.
 */

let answer: NetworkPolicyCoverage

vi.mock('../api/client', () => ({
  fetchNetworkPolicyCoverage: async () => answer,
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

const cluster = { id: 4, name: 'prod' } as Cluster

function coverage(overrides: Partial<NetworkPolicyCoverage>): NetworkPolicyCoverage {
  return {
    namespace: 'shop',
    policy_count: 1,
    pod_count: 6,
    ingress_covered_pods: 0,
    ingress_uncovered_pods: 6,
    egress_covered_pods: 0,
    egress_uncovered_pods: 6,
    available: true,
    disclaimer: 'Declared, not enforced.',
    ...overrides,
  }
}

afterEach(cleanup)

describe('network policy coverage', () => {
  it('draws a partly covered direction as danger', async () => {
    answer = coverage({ ingress_covered_pods: 4, ingress_uncovered_pods: 2 })
    render(<NetworkPolicyCoveragePanel cluster={cluster} namespace="shop" />)

    const bar = await screen.findByRole('img', { name: '4 covered, 2 uncovered' })
    expect(bar.lastElementChild?.className).toContain('bg-danger')
  })

  it('draws a direction no policy reaches in the quiet tone', async () => {
    answer = coverage({ policy_count: 0 })
    render(<NetworkPolicyCoveragePanel cluster={cluster} namespace="shop" />)

    expect(await screen.findByText('No NetworkPolicy in shop, 6 pods running.', { exact: false }))
      .toBeTruthy()
    for (const bar of screen.getAllByRole('img', { name: '0 covered, 6 uncovered' })) {
      expect(bar.lastElementChild?.className).toContain('bg-faint')
      expect(bar.lastElementChild?.className).not.toContain('bg-danger')
    }
  })

  it('counts the rest against the examples it shows, not the ones it was sent', async () => {
    answer = coverage({
      ingress_uncovered_examples: ['a', 'b', 'c', 'd', 'e'],
      egress_uncovered_pods: 0,
      egress_covered_pods: 6,
    })
    render(<NetworkPolicyCoveragePanel cluster={cluster} namespace="shop" />)

    const shown = await screen.findByText('a, b, c')
    expect(shown.parentElement?.textContent).toContain('and 3 more')
  })
})
