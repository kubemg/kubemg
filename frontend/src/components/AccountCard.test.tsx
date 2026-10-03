/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { AccountCard } from './AccountCard'

/*
 * The card's one rule worth a DOM: on a short screen the two personal doors
 * fold, and nothing else does — Administration stays, and a folded door that
 * is the current page is drawn anyway.
 */

function stubViewport(short: boolean) {
  vi.stubGlobal(
    'matchMedia',
    (query: string) =>
      ({
        matches: short && query.includes('max-height'),
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList,
  )
}

function renderCard({
  pathname = '/',
  isAdmin = true,
  inAdmin = false,
}: { pathname?: string; isAdmin?: boolean; inAdmin?: boolean } = {}) {
  return render(
    <MemoryRouter initialEntries={[pathname]}>
      <AccountCard
        userId={7}
        username="admin"
        isAdmin={isAdmin}
        inAdmin={inAdmin}
        pathname={pathname}
        back={{ to: '/', label: 'Back to the fleet' }}
      />
    </MemoryRouter>,
  )
}

beforeEach(() => window.localStorage.clear())
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('AccountCard', () => {
  it('draws every door with no toggle on a tall screen', () => {
    stubViewport(false)
    renderCard()
    expect(screen.getByText('My access')).toBeTruthy()
    expect(screen.getByText('My credentials')).toBeTruthy()
    expect(screen.getByText('Administration')).toBeTruthy()
    expect(screen.queryByRole('button', { expanded: false })).toBeNull()
  })

  it('folds the personal doors on a short screen, and keeps Administration', () => {
    stubViewport(true)
    renderCard()
    expect(screen.queryByText('My access')).toBeNull()
    expect(screen.queryByText('My credentials')).toBeNull()
    expect(screen.getByText('Administration')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { expanded: false }))
    expect(screen.getByText('My access')).toBeTruthy()
    expect(screen.getByText('My credentials')).toBeTruthy()
  })

  it('remembers the person opened them', () => {
    stubViewport(true)
    renderCard()
    fireEvent.click(screen.getByRole('button', { expanded: false }))
    cleanup()
    renderCard()
    expect(screen.getByText('My access')).toBeTruthy()
  })

  it('never hides the door that is the current page', () => {
    stubViewport(true)
    renderCard({ pathname: '/me/credentials' })
    expect(screen.getByText('My credentials')).toBeTruthy()
    expect(screen.queryByRole('button', { expanded: false })).toBeNull()
  })

  it('offers no toggle inside Administration, where the card is only the way back', () => {
    stubViewport(true)
    renderCard({ inAdmin: true })
    expect(screen.getByText('Back to the fleet')).toBeTruthy()
    expect(screen.queryByRole('button', { expanded: false })).toBeNull()
  })
})
