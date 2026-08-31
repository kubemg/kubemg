/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { Branding } from '../api/types'
import { BrandingContext } from '../state/branding-context'
import { EnvironmentBanner } from './EnvironmentBanner'

/*
 * The banner is the one thing in this console that renders before anybody has
 * signed in, and the one whose *absence* is load-bearing: a console that always
 * carries a banner is one where the banner stops being read. Both halves are
 * asserted here so neither drifts.
 */

function draw(branding: Branding | null) {
  return render(
    <BrandingContext.Provider value={{ branding, refresh: async () => {} }}>
      <EnvironmentBanner />
    </BrandingContext.Provider>,
  )
}

afterEach(cleanup)

describe('EnvironmentBanner', () => {
  it('draws nothing while the read is still in flight', () => {
    // A strip that arrives a beat late pushes the whole console down under the
    // operator's cursor, so nothing is drawn until the answer is known.
    const { container } = draw(null)
    expect(container.innerHTML).toBe('')
  })

  it('draws nothing for an install that has not configured one', () => {
    const { container } = draw({})
    expect(container.innerHTML).toBe('')
  })

  it('draws nothing for whitespace', () => {
    const { container } = draw({ banner_text: '   ' })
    expect(container.innerHTML).toBe('')
  })

  it('says what an administrator wrote', () => {
    draw({ banner_text: 'PRODUCTION — changes here affect customers' })
    expect(screen.getByText('PRODUCTION — changes here affect customers')).toBeTruthy()
  })

  it('is a status rather than an alert', () => {
    // A banner is a standing fact about the page, not an event that just
    // happened — an alert would be announced over whatever a screen reader was
    // in the middle of, on every navigation.
    draw({ banner_text: 'STAGING', banner_tone: 'caution' })
    expect(screen.getByRole('status').textContent).toBe('STAGING')
  })

  it('takes the tone it was given, and never the accent', () => {
    draw({ banner_text: 'PRODUCTION', banner_tone: 'critical' })
    const banner = screen.getByRole('status')
    expect(banner.className).toContain('danger')
    // Lime is the interactive accent and a banner is not pressable.
    expect(banner.className).not.toContain('accent')
  })

  it('falls back to neutral for a tone it does not know', () => {
    draw({ banner_text: 'PRODUCTION', banner_tone: 'chartreuse' as Branding['banner_tone'] })
    const banner = screen.getByRole('status')
    expect(banner.className).not.toContain('danger')
    expect(banner.className).not.toContain('warn')
  })
})
