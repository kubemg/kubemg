/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ShellState } from '../api/types'
import { ShellDockContext } from '../state/shell-dock-context'
import type { ShellDockState } from '../state/shell-dock-context'
import { ShellDock } from './ShellDock'

/*
 * #360: with agents given an address of their own, the shell's kubectl still
 * dials the public URL from inside the cluster. The dock says so, because a
 * prompt whose every kubectl hangs says nothing by itself.
 */

const shell = vi.hoisted(() => ({ next: null as ShellState | null }))
vi.mock('../api/client', () => ({
  startShell: () => Promise.resolve(shell.next),
  fetchShell: () => Promise.resolve(shell.next),
  stopShell: () => Promise.resolve(),
  errorMessage: (_cause: unknown, fallback: string) => fallback,
}))

afterEach(cleanup)

function starting(kubectl_url?: string): ShellState {
  return {
    enabled: true,
    available: true,
    idle_timeout_seconds: 900,
    max_lifetime_seconds: 3600,
    recorded: true,
    kubectl_url,
    status: { exists: true, ready: false, phase: 'Pending' },
  }
}

const dock: ShellDockState = {
  open: true,
  clusterId: 7,
  clusterName: 'edge',
  expanded: false,
  openOn: () => {},
  toggle: () => {},
  close: () => {},
  setExpanded: () => {},
}

function renderDock() {
  render(
    <ShellDockContext.Provider value={dock}>
      <ShellDock />
    </ShellDockContext.Provider>,
  )
}

describe('ShellDock', () => {
  it('says where kubectl dials when agents have their own address', async () => {
    shell.next = starting('https://kubemg.internal')
    renderDock()

    expect(await screen.findByText('https://kubemg.internal')).not.toBeNull()
    expect(screen.getByText(/not to the agent address/)).not.toBeNull()
  })

  it('says nothing of it when agents dial the public URL too', async () => {
    shell.next = starting()
    renderDock()

    expect(await screen.findByText(/The shell is starting/)).not.toBeNull()
    expect(screen.queryByText(/not to the agent address/)).toBeNull()
  })
})
