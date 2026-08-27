/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { Meter, Pill } from './primitives'

/*
 * The DOM half of the suite, kept deliberately small. What is worth rendering a
 * component for is a rule about the *output* that no pure function holds — the
 * meter's "no denominator" case is one, because the difference between drawing
 * nothing and drawing a full bar is the difference between "unknown" and "at
 * capacity", and a full bar for an unbounded reading is the exact mistake this
 * is here to catch.
 */

afterEach(cleanup)

describe('Meter', () => {
  it('reports its reading against a capacity when one bounds it', () => {
    render(<Meter label="CPU" value="120m" percent={48} capacity="250m" />)
    const meter = screen.getByRole('meter', { name: 'CPU' })
    expect(meter.getAttribute('aria-valuenow')).toBe('48')
    expect(meter.getAttribute('aria-valuetext')).toBe('120m of 250m')
    expect(screen.getByText('/ 250m')).toBeTruthy()
    expect(meter.firstElementChild?.getAttribute('style')).toContain('width: 48%')
  })

  it('draws a hatch rather than a full bar when nothing bounds it', () => {
    render(<Meter label="Memory" value="64Mi" />)
    const meter = screen.getByRole('meter', { name: 'Memory' })
    // Unknown is not the same as at capacity, and a full-width bar says the
    // second one.
    expect(meter.getAttribute('aria-valuenow')).toBeNull()
    expect(meter.getAttribute('aria-valuetext')).toBe('64Mi')
    expect(screen.getByText('no limit')).toBeTruthy()
    expect(meter.firstElementChild?.getAttribute('style')).toContain('repeating-linear-gradient')
    expect(meter.textContent).not.toContain('%')
  })

  it('clamps a reading past its own capacity to the track', () => {
    render(<Meter label="CPU" value="400m" percent={160} capacity="250m" />)
    const meter = screen.getByRole('meter', { name: 'CPU' })
    expect(meter.firstElementChild?.getAttribute('style')).toContain('width: 100%')
  })
})

describe('Pill', () => {
  it('writes a tone on its own soft plate, never fg on a tint', () => {
    // `tone on tone-soft` is the pairing the contrast pass measures; `fg` on a
    // tint is one nothing checks, which is why the pairing lives in one table.
    render(<Pill tone="bad">Failed</Pill>)
    const pill = screen.getByText('Failed')
    expect(pill.className).toContain('bg-danger-soft')
    expect(pill.className).toContain('text-danger')
  })
})
