import { describe, expect, it } from 'vitest'
import type { PostureFinding } from '../api/types'
import {
  NO_POSTURE_FILTER,
  POSTURE_CSV_COLUMNS,
  filterFindings,
  groupFindings,
  postureCsv,
  postureCsvFilename,
  severityDistribution,
  severityOf,
} from './posture'

function finding(over: Partial<PostureFinding> = {}): PostureFinding {
  return {
    rule: 'privileged_container',
    title: 'Privileged container',
    permits: 100,
    kind: 'Deployment',
    name: 'web',
    namespace: 'shop',
    field: 'spec.containers[0].securityContext.privileged',
    message: 'Runs privileged, which grants every capability root has on the node.',
    pss_covered: true,
    pss_profile: 'baseline',
    pss_control: 'Privileged Containers',
    acknowledged: false,
    ...over,
  }
}

describe('severityOf', () => {
  it('is derived from the ranking the server already asserts', () => {
    // The seven rules' own ranks, so a change on either side of the wire that
    // moved one across a band boundary shows up here.
    expect(severityOf({ permits: 100 })).toBe('critical') // privileged
    expect(severityOf({ permits: 90 })).toBe('critical') // host namespace
    expect(severityOf({ permits: 80 })).toBe('high') // hostPath
    expect(severityOf({ permits: 55 })).toBe('medium') // no NetworkPolicy
    expect(severityOf({ permits: 45 })).toBe('medium') // automounted default SA
    expect(severityOf({ permits: 30 })).toBe('low') // no non-root declaration
    expect(severityOf({ permits: 10 })).toBe('low') // no resource limits
  })

  it('bands an unknown rank by arithmetic rather than by a special case', () => {
    // A rule added on the server before this file learns about it still lands
    // somewhere sensible, which is the point of deriving from the number.
    expect(severityOf({ permits: 95 })).toBe('critical')
    expect(severityOf({ permits: 0 })).toBe('low')
  })
})

describe('severityDistribution', () => {
  it('reports every band, including the empty ones', () => {
    // A band that vanished when it was empty would make the strip's shape move
    // between scans, which is the one thing a distribution must not do.
    const counts = severityDistribution([finding()])
    expect(counts.map((c) => c.severity)).toEqual(['critical', 'high', 'medium', 'low'])
    expect(counts[1].total).toBe(0)
  })

  it('counts the work separately from the shape', () => {
    const counts = severityDistribution([
      finding(),
      finding({ name: 'api', acknowledged: true }),
    ])
    // Two critical findings; one of them is a decision somebody already made.
    expect(counts[0].total).toBe(2)
    expect(counts[0].open).toBe(1)
  })
})

describe('filterFindings', () => {
  it('hides acknowledged findings by default', () => {
    const rows = [finding(), finding({ name: 'api', acknowledged: true })]
    expect(filterFindings(rows, NO_POSTURE_FILTER)).toHaveLength(1)
    expect(
      filterFindings(rows, { ...NO_POSTURE_FILTER, showAcknowledged: true }),
    ).toHaveLength(2)
  })

  it('narrows to one band', () => {
    const rows = [finding(), finding({ name: 'api', permits: 10, title: 'No resource limits' })]
    expect(filterFindings(rows, { ...NO_POSTURE_FILTER, severity: 'low' })).toHaveLength(1)
  })

  it('searches the object, the namespace, the rule and the field', () => {
    const rows = [finding(), finding({ name: 'api', namespace: 'payments' })]
    expect(filterFindings(rows, { ...NO_POSTURE_FILTER, search: 'payments' })).toHaveLength(1)
    expect(filterFindings(rows, { ...NO_POSTURE_FILTER, search: 'privileged' })).toHaveLength(2)
    expect(filterFindings(rows, { ...NO_POSTURE_FILTER, search: 'securityContext' })).toHaveLength(2)
  })

  it('does not search the message', () => {
    // A substring search over prose matches almost everything, which makes the
    // box feel broken rather than useful.
    const rows = [finding()]
    expect(filterFindings(rows, { ...NO_POSTURE_FILTER, search: 'capability' })).toHaveLength(0)
  })
})

describe('groupFindings', () => {
  it('keeps the server order when nothing is grouped', () => {
    const rows = [finding(), finding({ name: 'api', permits: 10 })]
    const groups = groupFindings(rows, 'none')
    expect(groups).toHaveLength(1)
    expect(groups[0].findings).toEqual(rows)
  })

  it('orders severity groups worst first', () => {
    const rows = [finding({ permits: 10 }), finding({ permits: 100 })]
    expect(groupFindings(rows, 'severity').map((g) => g.key)).toEqual(['critical', 'low'])
  })

  it('leads with the namespace carrying the worst finding', () => {
    // The input is already ranked, so a group's first row is its worst one.
    const rows = [
      finding({ namespace: 'quiet', permits: 100 }),
      finding({ namespace: 'noisy', permits: 10 }),
      finding({ namespace: 'noisy', permits: 10, name: 'b' }),
    ]
    expect(groupFindings(rows, 'namespace').map((g) => g.label)).toEqual(['quiet', 'noisy'])
  })

  it('names a cluster-scoped finding rather than filing it under an empty string', () => {
    const rows = [finding({ namespace: undefined, kind: 'Namespace' })]
    expect(groupFindings(rows, 'namespace')[0].label).toBe('cluster-scoped')
  })

  it('never re-sorts inside a group', () => {
    // Re-sorting a group alphabetically would bury its worst row.
    const rows = [
      finding({ namespace: 'shop', name: 'zebra', permits: 100 }),
      finding({ namespace: 'shop', name: 'alpha', permits: 10 }),
    ]
    expect(groupFindings(rows, 'namespace')[0].findings.map((f) => f.name)).toEqual([
      'zebra',
      'alpha',
    ])
  })
})

describe('postureCsv', () => {
  it('leads with the header and one row per finding', () => {
    const lines = postureCsv([finding()]).split('\n')
    expect(lines[0]).toBe(POSTURE_CSV_COLUMNS.join(','))
    expect(lines).toHaveLength(2)
    expect(lines[1]).toContain('critical')
  })

  it('quotes a message that carries a comma', () => {
    // A finding's message routinely does, and an acknowledgement reason is free
    // text somebody typed.
    const csv = postureCsv([finding({ message: 'Runs privileged, which grants everything.' })])
    expect(csv).toContain('"Runs privileged, which grants everything."')
  })

  it('doubles an embedded quote', () => {
    const csv = postureCsv([finding({ ack_reason: 'agreed with the "platform" team' })])
    expect(csv).toContain('"agreed with the ""platform"" team"')
  })

  it('reads the PSS classification from pss_covered, never from the profile', () => {
    // The same rule the badge follows, so an export cannot classify a finding
    // differently from the row it came from.
    const uncovered = postureCsv([
      finding({ pss_covered: false, pss_profile: undefined, pss_control: undefined }),
    ])
    expect(uncovered).toContain('not a PSS control')
  })
})

describe('postureCsvFilename', () => {
  it('says what the file is of and when it was taken', () => {
    const at = new Date('2026-08-30T09:15:00Z')
    expect(postureCsvFilename('prod-eu', 'shop', at)).toBe('posture-prod-eu-shop-2026-08-30-09-15-00.csv')
  })

  it('names the whole scope when no namespace was chosen', () => {
    const at = new Date('2026-08-30T09:15:00Z')
    expect(postureCsvFilename('prod-eu', 'all', at)).toContain('all-namespaces')
  })
})
