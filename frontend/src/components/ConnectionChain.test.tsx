/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { Cluster } from '../api/types'
import { ConnectionChain } from './ConnectionChain'

/*
 * The three cases the issue asked for: an agent cluster whose tunnel is open,
 * one whose tunnel is not, and a direct-mode cluster — each one has to read
 * correctly off nothing but the `Cluster` object, since this is the same
 * component the fleet page renders once per row with no read of its own.
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

describe('ConnectionChain', () => {
  it('reads as a live tunnel when the agent is attached', () => {
    render(<ConnectionChain cluster={cluster({ connection_mode: 'agent', agent_attached: true })} username="dev" />)

    expect(screen.getByText('Tunnel open')).toBeTruthy()
    expect(screen.getByText('outbound tunnel · open')).toBeTruthy()
    // The proxy leg is agent mode's own promise regardless of the tunnel's
    // current state — that half of the chain is what the mode is, not what
    // the tunnel happens to be doing this second.
    expect(screen.getByText('Proxied')).toBeTruthy()
    expect(screen.getByText('proxied · audited')).toBeTruthy()
  })

  it('reads as not connected when an agent cluster has no tunnel and is otherwise healthy', () => {
    render(
      <ConnectionChain
        cluster={cluster({ connection_mode: 'agent', agent_attached: false, status: 'pending' })}
        username="dev"
      />,
    )

    expect(screen.getByText('Waiting')).toBeTruthy()
    expect(screen.getByText('outbound tunnel · not connected')).toBeTruthy()
  })

  it('reads as direct, with the kubeconfig captions, for a direct-mode cluster', () => {
    render(
      <ConnectionChain
        cluster={cluster({ connection_mode: 'direct', agent_attached: false, status: 'healthy' })}
        username="dev"
      />,
    )

    expect(screen.getByText('Direct')).toBeTruthy()
    expect(screen.getByText('kubemg dials the API server')).toBeTruthy()
    expect(screen.getByText('Kubeconfig')).toBeTruthy()
    expect(screen.getByText('kubeconfig · not proxied')).toBeTruthy()
  })

  it('names the cluster and the caller as the chain’s two endpoints', () => {
    render(<ConnectionChain cluster={cluster({ name: 'edge-us' })} username="devops" />)

    expect(screen.getByText('edge-us')).toBeTruthy()
    expect(screen.getByText('devops')).toBeTruthy()
  })
})
