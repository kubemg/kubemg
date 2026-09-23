// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'

import { Age } from './primitives'
import { formatInstant, relativeAge } from '../lib/time'

describe('Age', () => {
  it('renders the relative form as visible text', () => {
    const iso = new Date(Date.now() - 5 * 60_000).toISOString()
    render(<Age iso={iso} />)
    expect(screen.getByText(relativeAge(iso))).toBeDefined()
  })

  it('sets the title to formatInstant so hovering shows the exact instant', () => {
    const iso = new Date(Date.now() - 3 * 3_600_000).toISOString()
    render(<Age iso={iso} />)
    const el = screen.getByText(relativeAge(iso))
    expect(el.getAttribute('title')).toBe(formatInstant(iso))
  })

  it('sets dateTime to the original ISO string', () => {
    const iso = new Date(Date.now() - 86_400_000).toISOString()
    render(<Age iso={iso} />)
    const el = screen.getByText(relativeAge(iso))
    expect(el.getAttribute('datetime')).toBe(iso)
  })

  it('renders "never" for undefined without a time element', () => {
    const { container } = render(<Age iso={undefined} />)
    expect(screen.getByText('never')).toBeDefined()
    expect(container.querySelector('time')).toBeNull()
  })

  it('renders future timestamps correctly', () => {
    const iso = new Date(Date.now() + 20 * 3_600_000).toISOString()
    render(<Age iso={iso} />)
    expect(screen.getByText('in 20h')).toBeDefined()
  })
})
