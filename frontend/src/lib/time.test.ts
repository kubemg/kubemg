import { describe, expect, it } from 'vitest'

import { formatClock, formatInstant } from './time'

/*
 * The one rule these hold to is that the reading does not depend on where the
 * reader's machine thinks it is: ISO order and a 24-hour clock, with the zone
 * said out loud, so that `2026-08-26` is never read as August the twenty-sixth
 * by one auditor and the twenty-sixth of August by another off the same
 * screenshot. The tests are written against a fixed local instant rather than a
 * fixed offset, because the suite runs in whatever zone the container has.
 */

const at = new Date(2026, 7, 26, 19, 28, 22) // 26 Aug 2026, 19:28:22 local

describe('formatInstant', () => {
  it('writes ISO order, a 24-hour clock and the zone', () => {
    expect(formatInstant(at)).toMatch(/^2026-08-26 19:28 (UTC|UTC[+-]\d{2}:\d{2})$/)
  })

  it('leaves seconds out unless they are asked for', () => {
    expect(formatInstant(at)).toContain('19:28 ')
    expect(formatInstant(at, { seconds: true })).toContain('19:28:22 ')
  })

  it('never prints an AM/PM or a slash-separated date', () => {
    const rendered = formatInstant(at, { seconds: true })
    expect(rendered).not.toMatch(/[AP]M/i)
    expect(rendered).not.toContain('/')
  })

  it('says so rather than rendering an invalid date', () => {
    expect(formatInstant(undefined)).toBe('unknown')
    expect(formatInstant('not a date')).toBe('unknown')
    expect(formatClock('')).toBe('unknown')
  })
})

describe('formatClock', () => {
  it('is the same clock without the date', () => {
    expect(formatClock(at)).toBe('19:28')
    expect(formatClock(at, { seconds: true })).toBe('19:28:22')
  })
})
