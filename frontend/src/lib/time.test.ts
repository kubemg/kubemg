import { describe, expect, it } from 'vitest'

import { formatClock, formatInstant, relativeAge } from './time'

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

describe('relativeAge', () => {
  it('says never for undefined', () => {
    expect(relativeAge(undefined)).toBe('never')
  })

  it('says never for an unparseable string', () => {
    expect(relativeAge('not a date')).toBe('never')
  })

  it('says just now for a very recent past timestamp', () => {
    const iso = new Date(Date.now() - 10_000).toISOString()
    expect(relativeAge(iso)).toBe('just now')
  })

  it('says just now for a timestamp a few seconds ahead', () => {
    const iso = new Date(Date.now() + 10_000).toISOString()
    expect(relativeAge(iso)).toBe('just now')
  })

  it('rounds to minutes for past timestamps under an hour', () => {
    const iso = new Date(Date.now() - 5 * 60_000).toISOString()
    expect(relativeAge(iso)).toBe('5m ago')
  })

  it('rounds to hours for past timestamps under a day', () => {
    const iso = new Date(Date.now() - 3 * 3_600_000).toISOString()
    expect(relativeAge(iso)).toBe('3h ago')
  })

  it('rounds to days for older past timestamps', () => {
    const iso = new Date(Date.now() - 12 * 86_400_000).toISOString()
    expect(relativeAge(iso)).toBe('12d ago')
  })

  it('says "in Xm" for a timestamp 12 minutes in the future', () => {
    const iso = new Date(Date.now() + 12 * 60_000).toISOString()
    expect(relativeAge(iso)).toBe('in 12m')
  })

  it('says "in Xh" for a timestamp 20 hours in the future', () => {
    const iso = new Date(Date.now() + 20 * 3_600_000).toISOString()
    expect(relativeAge(iso)).toBe('in 20h')
  })

  it('says "in Xd" for a timestamp 3 days in the future', () => {
    const iso = new Date(Date.now() + 3 * 86_400_000).toISOString()
    expect(relativeAge(iso)).toBe('in 3d')
  })

  it('composes into "Expires in 20h" for a future kubeconfig expiry', () => {
    const iso = new Date(Date.now() + 20 * 3_600_000).toISOString()
    expect(`Expires ${relativeAge(iso)}`).toBe('Expires in 20h')
  })
})
