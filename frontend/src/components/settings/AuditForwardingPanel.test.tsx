/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuditForwarder, AuditForwarderList, AuditForwarderTest } from '../../api/types'
import { AuditForwardingPanel } from './AuditForwardingPanel'

/*
 * What is asserted here is the panel's honesty, not its layout.
 *
 * Three things about a forwarder are easy to get wrong in a way nobody notices
 * until an investigation has no records to run on: an empty list must not read
 * as "the trail is not being kept", a failed delivery must be visible without
 * opening anything, and a UDP test must not let a green tick be read as proof
 * of delivery.
 */

let list: AuditForwarderList = { forwarders: [], kinds: ['syslog'], protocols: ['tcp', 'udp', 'tls'] }
let verdict: AuditForwarderTest = { ok: true, message: 'The collector accepted a test record.' }

vi.mock('../../api/client', () => ({
  fetchAuditForwarders: () => Promise.resolve(list),
  testAuditForwarder: () => Promise.resolve(verdict),
  createAuditForwarder: () => Promise.resolve(forwarder()),
  updateAuditForwarder: () => Promise.resolve(forwarder()),
  deleteAuditForwarder: () => Promise.resolve(),
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

vi.mock('../../state/confirm-context', () => ({ useConfirm: () => async () => true }))
vi.mock('../../state/result-context', () => ({ useResult: () => () => {} }))

function forwarder(over: Partial<AuditForwarder> = {}): AuditForwarder {
  return {
    id: 1,
    name: 'logsign',
    kind: 'syslog',
    host: 'logsign.example.com',
    port: 515,
    protocol: 'tcp',
    facility: 16,
    app_name: 'kubemg',
    octet_counting: false,
    tls_insecure_skip_verify: false,
    enabled: true,
    created_at: '2026-08-25T09:00:00Z',
    updated_at: '2026-08-25T09:00:00Z',
    ...over,
  }
}

beforeEach(() => {
  list = { forwarders: [], kinds: ['syslog'], protocols: ['tcp', 'udp', 'tls'] }
  verdict = { ok: true, message: 'The collector accepted a test record.' }
})
afterEach(cleanup)

describe('AuditForwardingPanel', () => {
  it('says the trail is still complete when nothing is being pushed', async () => {
    render(<AuditForwardingPanel />)
    // "No destinations" must not read as "no audit trail" — the table and the
    // server's own log are unaffected, and the empty state has to say so.
    await waitFor(() => {
      expect(screen.getByText(/trail is still complete/i)).toBeTruthy()
    })
  })

  it('shows a failed delivery without anything being opened', async () => {
    list = {
      ...list,
      forwarders: [
        forwarder({
          last_status: 'error',
          last_message: 'dial tcp 10.0.0.9:515: connection refused',
          last_attempt_at: new Date().toISOString(),
        }),
      ],
    }
    render(<AuditForwardingPanel />)
    await waitFor(() => {
      expect(screen.getByText(/connection refused/)).toBeTruthy()
    })
  })

  it('states what a udp test cannot prove', async () => {
    list = { ...list, forwarders: [forwarder({ protocol: 'udp', port: 514 })] }
    verdict = {
      ok: true,
      message: 'The collector accepted a test record.',
      note: 'udp is fire-and-forget: this proves the address resolved, not that a collector received the record.',
    }
    render(<AuditForwardingPanel />)

    const button = await screen.findByRole('button', { name: /test/i })
    fireEvent.click(button)

    await waitFor(() => {
      expect(screen.getByText(/fire-and-forget/i)).toBeTruthy()
    })
  })

  it('warns on the row when a tls destination is not verifying its collector', async () => {
    list = {
      ...list,
      forwarders: [forwarder({ protocol: 'tls', tls_insecure_skip_verify: true })],
    }
    render(<AuditForwardingPanel />)
    await waitFor(() => {
      expect(screen.getByText(/certificate not verified/i)).toBeTruthy()
    })
  })
})
