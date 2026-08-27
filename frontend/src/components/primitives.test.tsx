/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { Meter, OBJECT_NAME, Pill, Row } from './primitives'

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

describe('an object name', () => {
  /*
   * The affordance is the underline, not the colour: lime is the deck's only
   * interactive accent and a list is thirty rows long, so the rest state says
   * "this addresses something" with a hairline and saves the accent for hover.
   * A name that opens nothing must not wear one — that is the whole rule.
   */
  it('states at rest that it addresses something', () => {
    expect(OBJECT_NAME).toContain('underline')
    expect(OBJECT_NAME).toContain('decoration-accent-line')
    expect(OBJECT_NAME).toContain('cursor-pointer')
    // At rest the name is `fg`: an accent-coloured name in every row spends the
    // accent on the one thing already certain to be clicked.
    expect(OBJECT_NAME.split(' ')).toContain('text-fg')
    expect(OBJECT_NAME.split(' ')).not.toContain('text-accent')
  })

  it('answers for a hover anywhere on its row, not only on the text', () => {
    render(
      <table>
        <tbody>
          <Row>
            <td>
              <button type="button" className={OBJECT_NAME}>
                argocd-redis-65748c6d4c-mts4k
              </button>
            </td>
          </Row>
        </tbody>
      </table>,
    )
    const row = screen.getByRole('row')
    expect(row.className).toContain('group/row')
    expect(row.className).toContain('focus-within:bg-raised')
    // The name is the group's subject, so the row's hover reaches it.
    expect(screen.getByRole('button').className).toContain('group-hover/row:text-accent')
  })
})
