/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CLISession } from '../api/types'
import { ConfirmContext } from '../state/confirm-context'
import { ResultContext } from '../state/result-context'
import { CliSessionsPanel } from './CliSessionsPanel'

const client = vi.hoisted(() => ({ fetchCLISessions: vi.fn(), revokeCLISession: vi.fn() }))
vi.mock('../api/client', () => ({ ...client, errorMessage: (_e: unknown, f: string) => f }))

const live: CLISession = {
  id: 's-1',
  user_id: 3,
  username: 'dana',
  client_host: 'laptop-17',
  client_os: 'darwin/arm64',
  source_ip: '203.0.113.9',
  expires_at: '2099-01-01T00:00:00Z',
  created_at: '2026-10-16T12:00:00Z',
  status: 'active',
}

async function draw(props: Partial<Parameters<typeof CliSessionsPanel>[0]> = {}) {
  render(
    <ConfirmContext.Provider value={async () => true}>
      <ResultContext.Provider value={() => {}}>
        <CliSessionsPanel showHolder={false} activeOnly {...props} />
      </ResultContext.Provider>
    </ConfirmContext.Provider>,
  )
  await act(async () => {})
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('the CLI sessions in the register', () => {
  it('asks for the account it is narrowed to', async () => {
    client.fetchCLISessions.mockResolvedValue([live])
    await draw({ userId: 3 })
    expect(client.fetchCLISessions).toHaveBeenCalledWith({ userId: 3, activeOnly: true })
    expect(screen.getByText('laptop-17')).toBeTruthy()
    // The holder column is the fleet reading's.
    expect(screen.queryByText('dana')).toBeNull()
  })

  it('ends a live session and re-reads', async () => {
    client.fetchCLISessions.mockResolvedValue([live])
    client.revokeCLISession.mockResolvedValue({ ...live, status: 'revoked' })
    await draw({ showHolder: true })
    expect(screen.getByText('dana')).toBeTruthy()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'End' }))
    })
    expect(client.revokeCLISession).toHaveBeenCalledWith('s-1')
    expect(client.fetchCLISessions).toHaveBeenCalledTimes(2)
  })

  it('offers nothing on a session that has ended', async () => {
    client.fetchCLISessions.mockResolvedValue([{ ...live, status: 'revoked' }])
    await draw()
    expect(screen.queryByRole('button', { name: 'End' })).toBeNull()
  })
})
