/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ArcGauge, Disclosure, Meter, OBJECT_NAME, Pill, Row, Switch, TickMeter } from './primitives'

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

describe('ArcGauge', () => {
  it('lights its share of the dial and says the reading in words', () => {
    const { container } = render(
      <ArcGauge label="CPU" value="2.00 cores" percent={50} capacity="4.00 cores" free="2.00 cores" />,
    )
    const gauge = screen.getByRole('meter', { name: 'CPU' })
    expect(gauge.getAttribute('aria-valuenow')).toBe('50')
    expect(gauge.getAttribute('aria-valuetext')).toBe('2.00 cores of 4.00 cores')
    const ticks = [...container.querySelectorAll('line')]
    expect(ticks.filter((tick) => tick.getAttribute('class')?.includes('stroke-ok'))).toHaveLength(
      ticks.length / 2,
    )
    expect(screen.getByText('free')).toBeTruthy()
  })

  it('lights a tick for a reading above zero however small', () => {
    const { container } = render(
      <ArcGauge label="CPU" value="4m" percent={0.1} capacity="4.00 cores" />,
    )
    const lit = [...container.querySelectorAll('line')].filter((tick) =>
      tick.getAttribute('class')?.includes('stroke-ok'),
    )
    expect(lit).toHaveLength(1)
  })

  it('takes the warning tone at the threshold the bar meter uses', () => {
    const { container } = render(
      <ArcGauge label="Memory" value="7 GiB" percent={80} capacity="8 GiB" />,
    )
    expect(container.querySelector('line.stroke-warn')).toBeTruthy()
    expect(container.querySelector('line.stroke-ok')).toBeNull()
  })

  it('lights nothing and claims no percentage when nothing bounds it', () => {
    const { container } = render(<ArcGauge label="Memory" value="64Mi" />)
    const gauge = screen.getByRole('meter', { name: 'Memory' })
    expect(gauge.getAttribute('aria-valuenow')).toBeNull()
    expect(screen.getByText('no limit')).toBeTruthy()
    expect(gauge.textContent).not.toContain('%')
    expect(container.querySelector('line:not(.stroke-line)')).toBeNull()
  })
})

describe('TickMeter', () => {
  it('lights its share of thirty ticks', () => {
    render(<TickMeter label="CPU" percent={50} detail="2.00 cores / 4.00 cores" />)
    const meter = screen.getByRole('meter', { name: 'CPU' })
    expect(meter.children).toHaveLength(30)
    expect([...meter.children].filter((tick) => tick.className.includes('bg-ok'))).toHaveLength(15)
    expect(screen.getByText('2.00 cores / 4.00 cores')).toBeTruthy()
  })

  it('draws a hatch rather than a full strip when nothing bounds it', () => {
    render(<TickMeter label="Memory" />)
    const meter = screen.getByRole('meter', { name: 'Memory' })
    expect(meter.children).toHaveLength(1)
    expect(meter.firstElementChild?.getAttribute('style')).toContain('repeating-linear-gradient')
  })
})

describe('Switch', () => {
  it('is a named switch that reports the flip rather than owning it', () => {
    const onChange = vi.fn()
    render(<Switch checked={false} onChange={onChange} label="Show Widget in the sidebar" />)
    const control = screen.getByRole('switch', { name: 'Show Widget in the sidebar' })
    expect(control.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(control)
    expect(onChange).toHaveBeenCalledWith(true)
  })
})

describe('Pill', () => {
  it('writes a tone on its own soft plate, never fg on a tint', () => {
    // `tone on tone-soft` is the pairing the contrast pass measures; `fg` on a
    // tint is one nothing checks, which is why the pairing lives in one table.
    render(<Pill tone="bad">Failed</Pill>)
    // The label is a truncating span inside the pill, so the plate is the
    // parent — which is also the element that has to be able to shrink.
    const pill = screen.getByText('Failed').parentElement
    expect(pill?.className).toContain('bg-danger-soft')
    expect(pill?.className).toContain('text-danger')
  })

  it('shrinks and ellipsises rather than pushing its neighbour out of the cell', () => {
    // A phase pill sits beside a ready count in one cell. Without `min-w-0` the
    // pill refuses to shrink and the count lands on the next column's value:
    // `2/2` beside `55m` rendered as `2/255m`.
    render(<Pill tone="bad">CrashLoopBackOff</Pill>)
    const label = screen.getByText('CrashLoopBackOff')
    expect(label.className).toContain('truncate')
    expect(label.parentElement?.className).toContain('min-w-0')
  })
})

describe('Disclosure', () => {
  it('renders closed with a summary line that names what is inside', () => {
    render(
      <Disclosure open={false} onOpenChange={() => {}} summary="Why this is like this">
        <p>The explanation.</p>
      </Disclosure>,
    )

    expect(screen.getByText('Why this is like this')).toBeTruthy()
    const details = screen.getByText('The explanation.').closest('details')
    expect(details?.hasAttribute('open')).toBe(false)
  })

  it('renders open when told to', () => {
    render(
      <Disclosure open={true} onOpenChange={() => {}} summary="Why this is like this">
        <p>The explanation.</p>
      </Disclosure>,
    )

    const details = screen.getByText('The explanation.').closest('details')
    expect(details?.hasAttribute('open')).toBe(true)
  })

  it('keeps its content in the DOM while closed, rather than unmounting it', () => {
    // find-in-page and a screen reader both need this to still be there —
    // the point of `hidden`, not a conditional `{open && ...}`.
    render(
      <Disclosure open={false} onOpenChange={() => {}} summary="Why this is like this">
        <p>Findable even while folded.</p>
      </Disclosure>,
    )

    expect(screen.getByText('Findable even while folded.')).toBeTruthy()
  })

  it('reports a toggle rather than owning the open state itself', () => {
    // The primitive draws the disclosure; `lib/disclosures.ts` is what
    // remembers it. Asserted here as a native `toggle`, the same event a
    // click on <summary> fires, so this holds regardless of how the toggle
    // was reached (click, keyboard, or a test that cannot simulate either).
    const onOpenChange = vi.fn()
    render(
      <Disclosure open={false} onOpenChange={onOpenChange} summary="Why this is like this">
        <p>Body</p>
      </Disclosure>,
    )

    const details = screen.getByText('Body').closest('details') as HTMLDetailsElement
    details.open = true
    details.dispatchEvent(new Event('toggle', { bubbles: false }))

    expect(onOpenChange).toHaveBeenCalledWith(true)
  })
})

describe('an object name', () => {
  /*
   * A name in a list is the one string nobody controls the length of, so it
   * wraps — and the affordance therefore cannot be an underline, which a wrap
   * either halves or leaves a stub of. Weight carries it instead.
   */
  it('wraps rather than truncating, and carries no decoration of its own', () => {
    expect(OBJECT_NAME).toContain('[overflow-wrap:anywhere]')
    expect(OBJECT_NAME).toContain('cursor-pointer')
    expect(OBJECT_NAME).not.toContain('truncate')
    expect(OBJECT_NAME).not.toContain('underline')
    // At rest the name is `fg`: an accent-coloured name in every row spends the
    // accent on the one thing already certain to be clicked.
    expect(OBJECT_NAME.split(' ')).toContain('text-fg')
    expect(OBJECT_NAME.split(' ')).not.toContain('text-accent')
    expect(OBJECT_NAME.split(' ')).toContain('font-semibold')
  })

  it('answers for a hover anywhere on its row, not only on the text', () => {
    render(
      <table>
        <tbody>
          <Row>
            <td>
              <button type="button" className={OBJECT_NAME}>
                argocd-notifications-controller-7d7c69d4d8-nbttw
              </button>
            </td>
          </Row>
        </tbody>
      </table>,
    )
    const row = screen.getByRole('row')
    expect(row.className).toContain('group/row')
    expect(row.className).toContain('focus-within:bg-raised')
    expect(screen.getByRole('button').className).toContain('group-hover/row:text-accent')
  })
})
