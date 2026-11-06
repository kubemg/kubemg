/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AlarmSettingsPanel } from './AlarmSettingsPanel'
import { GuardrailSettingsPanel } from './GuardrailSettingsPanel'
import { HelmRepositoriesPanel } from './HelmRepositoriesPanel'

/*
 * Each admin panel's first-run empty state must carry an EmptyState title so the
 * operator can read what is missing, and an action button that fixes it — without
 * having to find the header button separately. The alarm-rules branch with no
 * channel is the exception: a rule with nowhere to deliver looks like coverage and
 * is not, so the EmptyState carries no action and the header button is disabled.
 *
 * "Multiple elements" errors arise because the panel header also carries an action
 * button with the same label; getAllByRole checks that at least one copy exists.
 */

vi.mock('../../state/confirm-context', () => ({ useConfirm: () => async () => true }))
vi.mock('../../state/result-context', () => ({ useResult: () => () => {} }))

afterEach(cleanup)

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
  it('renders the channel EmptyState title and an Add channel button', async () => {
    render(<AlarmSettingsPanel clusters={[]} />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /no channels yet/i })).toBeTruthy()
      // Both the header and the EmptyState carry the button; at least one must exist.
      expect(screen.getAllByRole('button', { name: /add channel/i }).length).toBeGreaterThan(0)
    })
  })

  it('renders "Add a channel first" and keeps the Add rule button disabled when channels are absent', async () => {
    render(<AlarmSettingsPanel clusters={[]} />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /add a channel first/i })).toBeTruthy()
      // The header button exists but must be disabled — no enabled path to add a rule.
      const addRuleBtn = screen.queryByRole('button', { name: /add rule/i })
      expect(addRuleBtn).not.toBeNull()
      expect((addRuleBtn as HTMLButtonElement).disabled).toBe(true)
    })
  })
})

describe('GuardrailSettingsPanel empty state', () => {
  it('renders the EmptyState title and a Presets button', async () => {
    render(<GuardrailSettingsPanel clusters={[]} />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /no guardrails yet/i })).toBeTruthy()
      expect(screen.getAllByRole('button', { name: /presets/i }).length).toBeGreaterThan(0)
    })
  })
})

describe('HelmRepositoriesPanel empty state', () => {
  it('renders the EmptyState title and an Add repository button', async () => {
    render(<HelmRepositoriesPanel />)
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /no repositories/i })).toBeTruthy()
      expect(screen.getAllByRole('button', { name: /add repository/i }).length).toBeGreaterThan(0)
    })
  })
})
