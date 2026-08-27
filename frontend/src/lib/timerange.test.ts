import { describe, expect, it } from 'vitest'

import {
  DEFAULT_TIME_RANGE,
  isTimeRange,
  queryRangeLabel,
  TIME_RANGES,
  timeRangeLabel,
} from './timerange'

describe('the time range vocabulary', () => {
  it('carries no way to compute a boundary', () => {
    // The browser never subtracts from its own clock: a surface sends the
    // preset's id and the server resolves it, which is what makes two charts
    // and a link in a ticket name the same instant.
    for (const range of TIME_RANGES) {
      expect(Object.keys(range).sort()).toEqual(['id', 'label', 'short'])
    }
  })

  it('opens on a day', () => {
    expect(DEFAULT_TIME_RANGE).toBe('24h')
    expect(isTimeRange(DEFAULT_TIME_RANGE)).toBe(true)
  })

  it('recognises only what the server resolves', () => {
    expect(isTimeRange('15m')).toBe(true)
    expect(isTimeRange('all')).toBe(true)
    expect(isTimeRange('90d')).toBe(false)
    expect(isTimeRange('')).toBe(false)
    expect(isTimeRange(null)).toBe(false)
    expect(isTimeRange(undefined)).toBe(false)
  })

  it('says what a query actually drew when the window is All time', () => {
    // A metrics backend has retention and the query builder caps a single query
    // at thirty days, so "All time" would be claiming more than it drew.
    expect(timeRangeLabel('all')).toBe('All time')
    expect(queryRangeLabel('all')).toBe('Last 30 days (the widest a query allows)')
    expect(queryRangeLabel('6h')).toBe(timeRangeLabel('6h'))
  })
})
