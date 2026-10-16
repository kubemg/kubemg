/**
 * @vitest-environment jsdom
 */
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CLIDownloads } from '../api/types'
import { CliCommands } from './CliCommands'

let answer: CLIDownloads = { available: false, downloads: [], releases_url: 'https://github.com/kubemg/kubemg/releases' }
vi.mock('../api/client', () => ({ fetchCLIDownloads: () => Promise.resolve(answer) }))

afterEach(cleanup)

async function draw(props: Parameters<typeof CliCommands>[0]) {
  render(<CliCommands {...props} />)
  await act(async () => {})
}

describe('the CLI commands', () => {
  it('are rendered from the server address and the cluster, never typed by the reader', async () => {
    await draw({ serverURL: 'https://kubemg.example.com', cluster: 'edge-us' })
    expect(screen.getByText('kubemg login --server https://kubemg.example.com')).toBeTruthy()
    expect(screen.getByText('kubemg update-kubeconfig --cluster edge-us')).toBeTruthy()
  })

  it('fall back to this console’s own address when the server names none', async () => {
    await draw({ serverURL: '' })
    expect(screen.getByText(`kubemg login --server ${window.location.origin}`)).toBeTruthy()
  })

  it('point at the releases page when this server carries no binaries', async () => {
    answer = { available: false, downloads: [], releases_url: 'https://github.com/kubemg/kubemg/releases' }
    await draw({ serverURL: 'https://kubemg.example.com', showInstall: true })
    expect(screen.getByRole('link', { name: 'releases page' }).getAttribute('href')).toContain('/releases')
  })

  it('offer this server’s own binaries when it carries them — the air-gapped path', async () => {
    answer = {
      available: true,
      releases_url: 'https://github.com/kubemg/kubemg/releases',
      downloads: [
        { os: 'linux', arch: 'amd64', path: '/downloads/cli/linux/amd64', size: 1, sha256: 'a'.repeat(64) },
        { os: 'darwin', arch: 'arm64', path: '/downloads/cli/darwin/arm64', size: 1, sha256: 'b'.repeat(64) },
      ],
    }
    await draw({ serverURL: 'https://kubemg.example.com', showInstall: true })
    expect(screen.queryByRole('link', { name: 'releases page' })).toBeNull()
    expect(screen.getByRole('link', { name: /Linux amd64/ }).getAttribute('href')).toBe('/downloads/cli/linux/amd64')
    expect(screen.getByText(/curl -fLo kubemg https:\/\/kubemg\.example\.com\/downloads\/cli\//)).toBeTruthy()
  })
})
