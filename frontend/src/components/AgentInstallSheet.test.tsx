/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AgentInstall, Cluster } from '../api/types'
import { AgentInstallSheet } from './AgentInstallSheet'

/*
 * Re-opening the install package for a cluster that already exists. What is
 * asserted is that it renders what the server returns for this cluster, that
 * the single-use URL is said to be single-use, that a package a rotation
 * already answered with is shown rather than fetched again, and that a failed
 * render says so rather than showing an empty sheet.
 */

const calls: number[] = []
let answer: () => Promise<AgentInstall> = async () => install()

vi.mock('../api/client', () => ({
  fetchAgentInstall: (id: number) => {
    calls.push(id)
    return answer()
  },
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

const cluster = { id: 7, name: 'prod' } as Cluster

function install(over: Partial<AgentInstall> = {}): AgentInstall {
  return {
    cluster_id: 7,
    cluster: 'prod',
    namespace: 'kubemg-system',
    image: 'ghcr.io/kubemg/kubemg-agent:test',
    bastion_url: 'https://kubemg.example.com',
    package_dir: 'kubemg-agent',
    agent_token: 'tok-secret',
    manifest_url: 'https://kubemg.example.com/install/kmgi_ticket/agent.yaml',
    archive_url: 'https://kubemg.example.com/install/kmgi_ticket/kustomize.tar.gz',
    download_expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
    apply_command: 'kubectl apply -f https://kubemg.example.com/install/kmgi_ticket/agent.yaml',
    kustomize_command: 'curl -sfL … | tar -xz\nkubectl apply -k kubemg-agent',
    manifest: 'apiVersion: v1\nkind: Namespace\n',
    files: {},
    ...over,
  }
}

beforeEach(() => {
  calls.length = 0
  answer = async () => install()
})
afterEach(cleanup)

describe('AgentInstallSheet', () => {
  it('renders the package the server returns for this cluster', async () => {
    render(<AgentInstallSheet cluster={cluster} onClose={() => {}} />)

    expect(calls).toEqual([7])
    await waitFor(() =>
      expect(
        screen.getByText(
          'kubectl apply -f https://kubemg.example.com/install/kmgi_ticket/agent.yaml',
        ),
      ).toBeTruthy(),
    )
    // The upgrade path is the point of the sheet, so it is stated, not implied.
    expect(screen.getByText(/upgraded/i)).toBeTruthy()
  })

  it('says the install URL works once, and offers a fresh one', async () => {
    render(<AgentInstallSheet cluster={cluster} onClose={() => {}} />)

    await waitFor(() => expect(screen.getByText(/works once/)).toBeTruthy())
    screen.getByRole('button', { name: 'New URL' }).click()
    await waitFor(() => expect(calls).toEqual([7, 7]))
  })

  it('never points a browser download at the single-use URL', async () => {
    const { container } = render(<AgentInstallSheet cluster={cluster} onClose={() => {}} />)

    await waitFor(() => expect(screen.getByText('Download YAML')).toBeTruthy())
    expect(container.ownerDocument.querySelector('a[href*="/install/"]')).toBeNull()
  })

  it('shows the package a rotation answered with, without fetching another', async () => {
    render(
      <AgentInstallSheet
        cluster={cluster}
        rotated={install({ apply_command: 'kubectl apply -f rotated' })}
        onClose={() => {}}
      />,
    )

    expect(screen.getByText('kubectl apply -f rotated')).toBeTruthy()
    expect(screen.getByText(/was rotated/)).toBeTruthy()
    expect(calls).toEqual([])
  })

  it('masks the registration token until it is asked for', async () => {
    render(<AgentInstallSheet cluster={cluster} onClose={() => {}} />)

    await waitFor(() => expect(screen.getByText('Registration token')).toBeTruthy())
    expect(screen.queryByText('tok-secret')).toBeNull()
  })

  it('explains a render that failed instead of showing an empty sheet', async () => {
    answer = async () => {
      throw new Error('nope')
    }
    render(<AgentInstallSheet cluster={cluster} onClose={() => {}} />)

    await waitFor(() =>
      expect(screen.getByText('Could not render the install package.')).toBeTruthy(),
    )
    expect(screen.queryByText('Registration token')).toBeNull()
  })
})
