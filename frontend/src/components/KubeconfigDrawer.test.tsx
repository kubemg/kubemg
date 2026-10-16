/**
 * @vitest-environment jsdom
 */
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Cluster, KubeconfigPolicy } from '../api/types'
import { KubeconfigDrawer } from './KubeconfigDrawer'

let policy: KubeconfigPolicy = { min_ttl_seconds: 600, default_ttl_seconds: 3600, max_ttl_seconds: 86400 }

vi.mock('../api/client', () => ({
  fetchKubeconfigPolicy: () => Promise.resolve(policy),
  generateKubeconfig: () => Promise.reject(new Error('not used')),
  errorMessage: (_e: unknown, f: string) => f,
}))

const agent = { id: 1, name: 'edge-us', connection_mode: 'agent', namespaces: [] } as unknown as Cluster
const direct = { id: 2, name: 'legacy', connection_mode: 'direct', namespaces: [] } as unknown as Cluster

async function draw(cluster: Cluster) {
  render(<KubeconfigDrawer cluster={cluster} onClose={() => {}} />)
  await act(async () => {})
}

afterEach(cleanup)

describe('the kubeconfig sheet under cli_only', () => {
  it('offers the CLI instead of a file for an agent-mode cluster', async () => {
    policy = { ...policy, issuance: 'cli_only', server_url: 'https://kubemg.example.com', cli_session_max_hours: 12 }
    await draw(agent)
    expect(screen.getByText('kubemg login --server https://kubemg.example.com')).toBeTruthy()
    expect(screen.getByText('kubemg update-kubeconfig --cluster edge-us')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /generate kubeconfig/i })).toBeNull()
  })

  it('still generates for a direct-mode cluster, which no CLI session governs', async () => {
    policy = { ...policy, issuance: 'cli_only' }
    await draw(direct)
    expect(screen.getByRole('button', { name: /generate kubeconfig/i })).toBeTruthy()
  })

  it('generates as before when files are allowed', async () => {
    policy = { ...policy, issuance: 'allowed' }
    await draw(agent)
    expect(screen.getByRole('button', { name: /generate kubeconfig/i })).toBeTruthy()
  })
})
