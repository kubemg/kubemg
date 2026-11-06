/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AlarmSettingsPanel } from './AlarmSettingsPanel'
import { GuardrailSettingsPanel } from './GuardrailSettingsPanel'
import { HelmRepositoriesPanel } from './HelmRepositoriesPanel'

/*
 * Each admin panel's first-run empty state must carry an EmptyState title and
 * the action that fixes it, so the operator does not have to find the header
 * button after reading the message. The alarm-rules branch with no channel is
 * the exception: a rule with nowhere to deliver looks like coverage and is not,
 * so there is no "Add rule" button when no channel exists.
 */

vi.mock('../../state/confirm-context', () => ({ useConfirm: () => async () => true }))
vi.mock('../../state/result-context', () => ({ useResult: () => () => {} }))

afterEach(cleanup)

/* ----------------------------------------------------------------- alarms --- */

vi.mock('../../api/client', () => ({
  fetchAlarmChannels: () => Promise.resolve({ channels: [], kinds: [] }),
  fetchAlarmRules: () => Promise.resolve({ rules: [], dispatcher_running: true }),
  fetchGuardrailPolicies: () =>
    Promise.resolve({ policies: [], targets: [], actions: [], enforcing: 0 }),
  fetchGuardrailTemplates: () => Promise.resolve([]),
  fetchHelmRepositories: () => Promise.resolve({ repositories: [] }),
  createAlarmChannel: vi.fn(),
  createAlarmRule: vi.fn(),
  updateAlarmChannel: vi.fn(),
  updateAlarmRule: vi.fn(),
  deleteAlarmChannel: vi.fn(),
  deleteAlarmRule: vi.fn(),
  testAlarmChannel: vi.fn(),
  createGuardrailPolicy: vi.fn(),
  updateGuardrailPolicy: vi.fn(),
  deleteGuardrailPolicy: vi.fn(),
  putHelmRepository: vi.fn(),
  deleteHelmRepository: vi.fn(),
  syncHelmRepository: vi.fn(),
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

describe('AlarmSettingsPanel empty states', () => {
  it('renders the channel EmptyState title and Add channel button', async () => {
    render(<AlarmSettingsPanel clusters={[]} />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /no channels yet/i })).toBeTruthy()
      expect(screen.getByRole('button', { name: /add channel/i })).toBeTruthy()
    })
  })

  it('renders "Add a channel first" with no rule-creation button when channels are absent', async () => {
    render(<AlarmSettingsPanel clusters={[]} />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /add a channel first/i })).toBeTruthy()
      expect(screen.queryByRole('button', { name: /add rule/i })).toBeNull()
    })
  })
})

describe('GuardrailSettingsPanel empty state', () => {
  it('renders the EmptyState title and Presets button', async () => {
    render(<GuardrailSettingsPanel clusters={[]} />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /no guardrails yet/i })).toBeTruthy()
      expect(screen.getByRole('button', { name: /presets/i })).toBeTruthy()
    })
  })
})

describe('HelmRepositoriesPanel empty state', () => {
  it('renders the EmptyState title and Add repository button', async () => {
    render(<HelmRepositoriesPanel />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /no repositories/i })).toBeTruthy()
      expect(screen.getByRole('button', { name: /add repository/i })).toBeTruthy()
    })
  })
})
