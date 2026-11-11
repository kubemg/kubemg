/**
 * @vitest-environment jsdom
 */
import type { ReactNode } from 'react'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Branding } from '../api/types'
import { BrandingContext } from '../state/branding-context'
import { AppFooter } from './AppFooter'
import { OrganisationIdentityView } from './OrganisationIdentity'

vi.mock('../api/client', () => ({
  fetchServerVersion: () => Promise.resolve({ version: 'v0.17.1', docs_url: 'https://kubemg.readthedocs.io/' }),
}))

/*
 * Whose installation this is: the organisation's name and mark sit beside the
 * lockup, and only there. The footer used to carry the name too, running on
 * from the release number as one unexplained line.
 */

afterEach(cleanup)

function withBranding(branding: Branding, children: ReactNode) {
  return (
    <BrandingContext.Provider value={{ branding, refresh: () => Promise.resolve() }}>
      {children}
    </BrandingContext.Provider>
  )
}

describe('OrganisationIdentityView', () => {
  it('draws nothing for an install nobody has branded', () => {
    const { container } = render(<OrganisationIdentityView tone="rail" name="  " mark="" />)
    expect(container.innerHTML).toBe('')
  })

  it('draws the trimmed name after a divider, with the whole name a hover away', () => {
    const { container } = render(<OrganisationIdentityView tone="rail" name="  Acme Platform Engineering " mark="" />)
    const name = screen.getByText('Acme Platform Engineering')
    expect(name.getAttribute('title')).toBe('Acme Platform Engineering')
    expect(name.className).toContain('truncate')
    expect(container.querySelector('[aria-hidden="true"].w-px')).not.toBeNull()
    expect(container.querySelector('img')).toBeNull()
  })

  it('draws a mark with no name, still behind the divider', () => {
    const { container } = render(<OrganisationIdentityView tone="page" name="" mark="data:image/png;base64,AAAA" />)
    expect(container.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,AAAA')
    expect(container.querySelector('[aria-hidden="true"].w-px')).not.toBeNull()
  })
})

describe('AppFooter', () => {
  it('carries the release and the handling notice, never the organisation name', async () => {
    render(
      withBranding(
        { organisation_name: 'Test Organization', footer_notice: 'Internal use only' },
        <AppFooter />,
      ),
    )
    expect(await screen.findByText('v0.17.1')).toBeTruthy()
    expect(screen.getByText('Internal use only')).toBeTruthy()
    expect(screen.queryByText('Test Organization')).toBeNull()
  })

  it('draws nothing for a name alone when the version read has not answered', () => {
    const { container } = render(withBranding({ organisation_name: 'Test Organization' }, <AppFooter />))
    // Before the read resolves, a name is no reason to draw a footer.
    expect(container.innerHTML).toBe('')
  })
})
