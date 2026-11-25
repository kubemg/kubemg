/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { EmptyState } from './primitives'

/*
 * An empty state is read in a glance: its title stays on the page and its
 * explanation moves one hover or focus away, unless the explanation is what the
 * reader needs in order to go on.
 */

afterEach(cleanup)

describe('EmptyState', () => {
  it('puts the explanation behind an ⓘ beside the title', () => {
    render(
      <EmptyState title="No activity yet">
        Proxied kubectl traffic against agent-based clusters lands here, refusals included.
      </EmptyState>,
    )

    expect(screen.getByRole('heading', { name: 'No activity yet' })).not.toBeNull()
    const tip = screen.getByRole('tooltip')
    expect(tip.textContent).toBe(
      'Proxied kubectl traffic against agent-based clusters lands here, refusals included.',
    )
    // The ⓘ is a real control a keyboard reaches, and it points a screen reader
    // at the sentence it stands for.
    const button = screen.getByRole('button', { name: 'About No activity yet' })
    expect(button.getAttribute('aria-describedby')).toBe(tip.id)
    expect(document.querySelector('p')).toBeNull()
  })

  it('keeps the explanation on the page when the reader needs it', () => {
    render(
      <EmptyState title="That cluster is not registered" detail="visible">
        Pick a cluster from the fleet list to read its alerts.
      </EmptyState>,
    )

    expect(screen.getByText('Pick a cluster from the fleet list to read its alerts.').tagName).toBe('P')
    expect(screen.queryByRole('tooltip')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('draws no ⓘ when there is nothing to explain', () => {
    render(<EmptyState title="Nothing decided yet" />)

    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByRole('tooltip')).toBeNull()
  })
})
