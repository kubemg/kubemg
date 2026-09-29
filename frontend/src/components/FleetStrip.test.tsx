/**
 * @vitest-environment jsdom
 */
import { MemoryRouter } from 'react-router'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { FleetStrip } from './FleetStrip'
import type { StripFigure } from '../lib/fleetStrip'

afterEach(cleanup)

function figure(over: Partial<StripFigure>): StripFigure {
  return { key: 'requests', value: 0, label: 'requests waiting', to: '/x', tone: 'warn', ...over }
}

describe('FleetStrip', () => {
  it('tells zero, a failed read and a read in flight apart', () => {
    render(
      <MemoryRouter>
        <FleetStrip
          figures={[
            figure({ key: 'requests', value: 0, label: 'requests waiting', to: '/a' }),
            figure({ key: 'refused', value: null, label: 'refused', to: '/b' }),
            figure({ key: 'expiring', value: undefined, label: 'expiring', to: '/c' }),
            figure({ key: 'behind', value: 3, label: 'agents behind', to: '/d' }),
          ]}
        />
      </MemoryRouter>,
    )

    const links = screen.getAllByRole('link')
    expect(links).toHaveLength(4)
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['/a', '/b', '/c', '/d'])

    expect(links[0].textContent).toContain('0')
    // A failed read is a dash and says so — it never draws a 0.
    expect(links[1].textContent).toContain('—')
    expect(links[1].textContent).toContain('could not be read')
    expect(links[1].textContent).not.toMatch(/\d/)
    // A read in flight is neither a number nor the failed dash.
    expect(links[2].textContent).toContain('…')
    expect(links[2].textContent).not.toMatch(/\d|—/)
    expect(links[3].textContent).toContain('3')
  })
})
