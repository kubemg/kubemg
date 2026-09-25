/**
 * @vitest-environment jsdom
 */
import { MemoryRouter } from 'react-router'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { Cluster } from '../api/types'
import { FleetConnectionChains } from './Overview'

/*
 * The fleet variant is the same chain, listed once per cluster, reading each
 * cluster's own link state off the same `Cluster` list the page already has —
 * no capacity, no second read, and no interaction beyond opening the cluster
 * a row names.
 */

function cluster(over: Partial<Cluster> = {}): Cluster {
  return {
    id: 1,
    name: 'edge-us',
    environment: 'prod',
    api_url: '',
    status: 'healthy',
    created_at: '2026-01-01T00:00:00Z',
    k8s_role: 'edit',
    namespaces: [],
    connection_mode: 'agent',
    agent_attached: true,
    ...over,
  }
}

afterEach(cleanup)

describe('FleetConnectionChains', () => {
  it('renders one row per cluster, each reflecting its own link state', () => {
    const clusters: Cluster[] = [
      cluster({ id: 1, name: 'edge-us', connection_mode: 'agent', agent_attached: true }),
      cluster({ id: 2, name: 'edge-eu', connection_mode: 'agent', agent_attached: false, status: 'pending' }),
      cluster({ id: 3, name: 'edge-apac', connection_mode: 'direct', status: 'healthy' }),
    ]

    render(
      <MemoryRouter>
        <FleetConnectionChains clusters={clusters} username="dev" />
      </MemoryRouter>,
    )

    expect(screen.getByText('edge-us')).toBeTruthy()
    expect(screen.getByText('edge-eu')).toBeTruthy()
    expect(screen.getByText('edge-apac')).toBeTruthy()

    // Three distinct link states, one per row — not the same state repeated.
    expect(screen.getByText('Tunnel open')).toBeTruthy()
    expect(screen.getByText('Waiting')).toBeTruthy()
    expect(screen.getByText('Direct')).toBeTruthy()

    // The only interaction offered is opening the cluster a row names.
    const links = screen.getAllByRole('link')
    expect(links).toHaveLength(3)
    expect(links[0].getAttribute('href')).toContain('/1/')
    expect(links[1].getAttribute('href')).toContain('/2/')
    expect(links[2].getAttribute('href')).toContain('/3/')
  })
})
