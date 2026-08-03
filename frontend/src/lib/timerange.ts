/*
 * The console's time range — one vocabulary, one control, one window.
 *
 * Every ranged surface used to carry its own preset table: `MetricsChart` had
 * four windows, `LogExplorer` five, the audit trail six and an "All". So two
 * charts side by side were independently scoped and could honestly disagree
 * about what "now" covered, and the same words meant different spans depending
 * on which card you read them in.
 *
 * Two rules fix that and neither is negotiable. **The vocabulary is the one the
 * server already resolves** (`pkg/api/timerange.go`) rather than a second table
 * that agrees with it by coincidence. And **the browser never computes the
 * boundary**: a surface sends the preset's id and the server subtracts from its
 * own clock, so a chart, a trail and a link somebody pastes into a ticket all
 * name the same instant. That is also why these entries carry no `minutes`
 * field — there is nothing here to subtract with, on purpose.
 *
 * `all` is the one entry whose meaning is a property of the surface: the audit
 * trail reads it as "no lower bound", while a query against a datasource reads
 * it as the widest window that path allows, because a metrics backend has
 * retention and thirty days is the cap the query builder enforces anyway.
 */

export const TIME_RANGES = [
  { id: '15m', label: 'Last 15 minutes', short: '15m' },
  { id: '1h', label: 'Last hour', short: '1h' },
  { id: '6h', label: 'Last 6 hours', short: '6h' },
  { id: '24h', label: 'Last 24 hours', short: '24h' },
  { id: '7d', label: 'Last 7 days', short: '7d' },
  { id: '30d', label: 'Last 30 days', short: '30d' },
  { id: 'all', label: 'All time', short: 'All' },
] as const

export type TimeRangeId = (typeof TIME_RANGES)[number]['id']

/**
 * The window the console opens on. A day rather than an hour, because the
 * surface most damaged by the wrong default is the audit trail: a chart over a
 * day is coarser than one over an hour, while a trail over an hour is usually
 * empty, and an empty page reads as a broken one.
 */
export const DEFAULT_TIME_RANGE: TimeRangeId = '24h'

/** The query parameter the window travels in, so a link carries what it was read at. */
export const TIME_RANGE_PARAM = 'range'

export function isTimeRange(value: string | null | undefined): value is TimeRangeId {
  return TIME_RANGES.some((entry) => entry.id === value)
}

export function timeRangeLabel(id: TimeRangeId): string {
  return TIME_RANGES.find((entry) => entry.id === id)?.label ?? id
}

/**
 * The same window, named for a surface that reads a datasource. `all` is the
 * only entry that differs: the query path caps a single query at thirty days,
 * so a chart claiming to draw all time would be claiming more than it drew.
 */
export function queryRangeLabel(id: TimeRangeId): string {
  return id === 'all' ? 'Last 30 days (the widest a query allows)' : timeRangeLabel(id)
}
