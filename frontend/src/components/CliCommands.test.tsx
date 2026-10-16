/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { CliCommands } from './CliCommands'

afterEach(cleanup)

describe('the CLI commands', () => {
  it('are rendered from the server address and the cluster, never typed by the reader', () => {
    render(<CliCommands serverURL="https://kubemg.example.com" cluster="edge-us" showInstall />)
    expect(screen.getByText('kubemg login --server https://kubemg.example.com')).toBeTruthy()
    expect(screen.getByText('kubemg update-kubeconfig --cluster edge-us')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'releases page' }).getAttribute('href')).toContain('/releases')
  })

  it('fall back to this console’s own address when the server names none', () => {
    render(<CliCommands serverURL="" />)
    expect(screen.getByText(`kubemg login --server ${window.location.origin}`)).toBeTruthy()
  })
})
