/**
 * @vitest-environment jsdom
 */
import { act, cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { User } from '../api/types'
import { AuthContext } from './auth-context'
import type { AuthState } from './auth-context'
import { ShellDockProvider } from './ShellDockProvider'
import { useShellDock } from './shell-dock-context'

function auth(user: User | null): AuthState {
  return {
    user,
    loading: false,
    signIn: async () => null,
    adoptSession: async () => {},
    signOut: () => {},
    replaceUser: () => {},
    setupRequired: false,
    setupLoading: false,
    refreshSetupState: async () => {},
  }
}

const ada = { id: 1, username: 'ada' } as User
const bob = { id: 2, username: 'bob' } as User

let dock: ReturnType<typeof useShellDock> | null = null
function Probe() {
  dock = useShellDock()
  return null
}

function tree(user: User | null) {
  return (
    <AuthContext.Provider value={auth(user)}>
      <ShellDockProvider>
        <Probe />
      </ShellDockProvider>
    </AuthContext.Provider>
  )
}

// A shell is its owner's session: signing out, or somebody else signing in
// at the same browser, closes the dock and forgets the cluster.
describe('the shell dock across identities', () => {
  afterEach(() => {
    cleanup()
    dock = null
  })

  it('closes and forgets the cluster when the user signs out or changes', () => {
    const view = render(tree(ada))
    act(() => dock?.openOn(7, 'prod'))
    expect(dock?.open).toBe(true)
    expect(dock?.clusterId).toBe(7)

    view.rerender(tree(null))
    expect(dock?.open).toBe(false)
    expect(dock?.clusterId).toBeNull()

    view.rerender(tree(ada))
    act(() => dock?.openOn(7, 'prod'))
    view.rerender(tree(bob))
    expect(dock?.open).toBe(false)
    expect(dock?.clusterId).toBeNull()
  })

  it('toggles the same cluster closed and another one open', () => {
    render(tree(ada))
    act(() => dock?.toggle(7, 'prod'))
    expect(dock?.open).toBe(true)
    act(() => dock?.toggle(7, 'prod'))
    expect(dock?.open).toBe(false)
    act(() => dock?.toggle(8, 'stage'))
    expect(dock?.open).toBe(true)
    expect(dock?.clusterId).toBe(8)
  })
})
