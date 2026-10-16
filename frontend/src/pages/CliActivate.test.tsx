/**
 * @vitest-environment jsdom
 */
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CLIDeviceRequest } from '../api/types'
import { CliActivateBody } from './CliActivate'

const client = vi.hoisted(() => ({
  fetchCLIDeviceRequest: vi.fn(),
  decideCLIDeviceRequest: vi.fn(),
}))

vi.mock('../api/client', () => ({
  ...client,
  errorMessage: (err: unknown, fallback: string) => (err instanceof Error && err.message) || fallback,
}))

const request: CLIDeviceRequest = {
  user_code: 'BCDF-GHJK',
  client_name: 'kubemg',
  client_version: '0.16.0',
  client_host: 'laptop-17',
  client_os: 'darwin/arm64',
  source_ip: '203.0.113.9',
  same_address: true,
  status: 'pending',
  created_at: '2026-10-16T12:00:00Z',
  expires_at: '2026-10-16T12:10:00Z',
  session_hours: 12,
}

async function draw(code: string, onCode = vi.fn(), extra: { account?: string; onSwitchAccount?: () => void } = {}) {
  render(
    <MemoryRouter>
      <CliActivateBody code={code} onCode={onCode} {...extra} />
    </MemoryRouter>,
  )
  await act(async () => {})
  return onCode
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('approving a CLI sign-in', () => {
  it('asks for the code when the link carried none', async () => {
    const onCode = await draw('')
    fireEvent.change(screen.getByLabelText('Code shown by kubemg login'), { target: { value: 'bcdf-ghjk' } })
    fireEvent.submit(screen.getByLabelText('Code shown by kubemg login').closest('form') as HTMLFormElement)
    expect(onCode).toHaveBeenCalledWith('bcdf-ghjk')
    expect(client.fetchCLIDeviceRequest).not.toHaveBeenCalled()
  })

  it('shows the machine, the address and the length before anything is approved', async () => {
    client.fetchCLIDeviceRequest.mockResolvedValue(request)
    await draw('BCDF-GHJK')
    expect(screen.getByText('laptop-17')).toBeTruthy()
    expect(screen.getByText('203.0.113.9')).toBeTruthy()
    expect(screen.getByText(/as you for 12 hours/)).toBeTruthy()
    // Same address: nothing to warn about.
    expect(screen.queryByText(/not from the address this browser is using/)).toBeNull()
    expect(client.decideCLIDeviceRequest).not.toHaveBeenCalled()
  })

  it('warns when the request came from somewhere else', async () => {
    client.fetchCLIDeviceRequest.mockResolvedValue({ ...request, same_address: false })
    await draw('BCDF-GHJK')
    expect(screen.getByText(/not from the address this browser is using/)).toBeTruthy()
  })

  it('approves, then says to go back to the terminal', async () => {
    client.fetchCLIDeviceRequest.mockResolvedValue(request)
    client.decideCLIDeviceRequest.mockResolvedValue(undefined)
    await draw('BCDF-GHJK')
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Approve sign-in' }))
    })
    expect(client.decideCLIDeviceRequest).toHaveBeenCalledWith('BCDF-GHJK', 'approve')
    expect(screen.getByText(/Go back to your terminal/)).toBeTruthy()
  })

  it('leads with the code to compare against the terminal, and names the account approving', async () => {
    client.fetchCLIDeviceRequest.mockResolvedValue(request)
    const onSwitchAccount = vi.fn()
    await draw('BCDF-GHJK', vi.fn(), { account: 'dana', onSwitchAccount })
    expect(screen.getByText('BCDF-GHJK')).toBeTruthy()
    expect(screen.getByText(/matches the one in your terminal/)).toBeTruthy()
    expect(screen.getAllByText('dana').length).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: 'Use another account' }))
    expect(onSwitchAccount).toHaveBeenCalled()
  })

  it('denies, and says nothing was signed in', async () => {
    client.fetchCLIDeviceRequest.mockResolvedValue(request)
    client.decideCLIDeviceRequest.mockResolvedValue(undefined)
    await draw('BCDF-GHJK')
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    })
    expect(client.decideCLIDeviceRequest).toHaveBeenCalledWith('BCDF-GHJK', 'deny')
    expect(screen.getByText('Sign-in denied')).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'My credentials' })).toBeNull()
  })

  it('says what the server refused, and offers the code box again for an expired one', async () => {
    client.fetchCLIDeviceRequest.mockRejectedValue(new Error('no sign-in is waiting for that code'))
    await draw('ZZZZ-ZZZZ')
    expect(screen.getByText('no sign-in is waiting for that code')).toBeTruthy()
    expect(screen.getByLabelText('Code shown by kubemg login')).toBeTruthy()
  })
})
