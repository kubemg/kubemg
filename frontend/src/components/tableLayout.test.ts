import { describe, expect, it } from 'vitest'

import auditTrail from '../pages/AuditTrail.tsx?raw'
import clusterManagement from '../pages/ClusterManagement.tsx?raw'
import deck from '../index.css?inline'
import detailDrawer from './ResourceDetailDrawer.tsx?raw'
import issuedCredentials from '../pages/IssuedCredentials.tsx?raw'
import primitives from './primitives.tsx?raw'
import resourceTables from './ResourceTables.tsx?raw'
import sessionRecordings from '../pages/SessionRecordings.tsx?raw'
import userManagement from '../pages/UserManagement.tsx?raw'
import workloadPods from './WorkloadPodsView.tsx?raw'

/*
 * Two rules about how a table is laid out, both of which were broken at once
 * and neither of which shows up in a unit test of any component — they are
 * properties of the class names, so they are asserted against the source.
 *
 * 1. **A percentage budget.** `table-fixed` honours a percentage width, and it
 *    honours all of them: a table whose sized columns ask for 122% squeezes
 *    every column, and one that asks for 93% while leaving the name column
 *    unsized gives the name 7% and wraps a pod name one character per line.
 *    Both were shipped, because the widths had never been read at all until the
 *    `min()` wrappers came off and every number became binding at once.
 *
 * 2. **A sticky offset of zero by default.** `--table-sticky-top` is only ever
 *    the page header's height where the *window* is the scrollport. Nearly
 *    every table here is inside a card that is `overflow-hidden` for its
 *    corners, and `overflow: hidden` is a scroll container: an offset resolved
 *    against the card pushes the heading down into the rows rather than pinning
 *    it, leaving an empty band where the heading belongs — at rest, with nobody
 *    having scrolled anything.
 */

const SOURCES: Record<string, string> = {
  'ResourceTables.tsx': resourceTables,
  'WorkloadPodsView.tsx': workloadPods,
  'ResourceDetailDrawer.tsx': detailDrawer,
  'AuditTrail.tsx': auditTrail,
  'ClusterManagement.tsx': clusterManagement,
  'IssuedCredentials.tsx': issuedCredentials,
  'SessionRecordings.tsx': sessionRecordings,
  'UserManagement.tsx': userManagement,
}

const BREAKPOINTS = ['base', 'sm', 'md', 'lg', 'xl'] as const
type Breakpoint = (typeof BREAKPOINTS)[number]

/** What one heading is worth at a breakpoint: a percentage, unsized, or gone. */
function widthAt(th: string, bp: Breakpoint): number | 'unsized' | 'hidden' {
  const upTo = BREAKPOINTS.slice(0, BREAKPOINTS.indexOf(bp) + 1)
  if (th.includes('hidden') && !upTo.some((b) => th.includes(`${b}:table-cell`))) return 'hidden'

  let width: number | 'unsized' = 'unsized'
  for (const match of th.matchAll(/(?:(sm|md|lg|xl):)?w-\[(\d+)%\]/g)) {
    const at = (match[1] ?? 'base') as Breakpoint
    if (upTo.includes(at)) width = Number(match[2])
  }
  return width
}

/** Every `<thead>` in a file, with the line it starts on so a failure names it. */
function heads(source: string): { line: number; ths: string[] }[] {
  const found: { line: number; ths: string[] }[] = []
  for (const match of source.matchAll(/<thead>([\s\S]*?)<\/thead>/g)) {
    found.push({
      line: source.slice(0, match.index).split('\n').length,
      ths: [...match[1].matchAll(/<(?:Sort)?Th\b[^>]*>/g)].map((m) => m[0]),
    })
  }
  return found
}

describe('the percentage budget every fixed table lives inside', () => {
  it.each(Object.keys(SOURCES))('%s asks for widths that fit', (file) => {
    const overdrawn: string[] = []

    for (const { line, ths } of heads(SOURCES[file])) {
      for (const bp of BREAKPOINTS) {
        let asked = 0
        let unsized = 0
        for (const th of ths) {
          const width = widthAt(th, bp)
          if (width === 'hidden') continue
          if (width === 'unsized') unsized += 1
          else asked += width
        }
        // Over 100 the browser squeezes every column at once, and the columns
        // that lose are the narrow ones — a heading ellipsised to `R…`.
        if (asked > 100) overdrawn.push(`${file}:${line} at ${bp}: ${asked}%`)
        // An unsized column takes what is left, and what is left has to be
        // enough to hold a name. 15% of a table is not.
        if (unsized > 0 && asked > 85) {
          overdrawn.push(`${file}:${line} at ${bp}: ${asked}% leaves ${100 - asked}% for a name`)
        }
      }
    }

    expect(overdrawn).toEqual([])
  })

  it('gives a column with a word in its heading room for the word', () => {
    // `Restarts` in 5% of a table is `R…`, which names nothing. Anything under
    // this is a column whose own heading does not fit in it.
    const cramped: string[] = []
    for (const [file, source] of Object.entries(SOURCES)) {
      for (const match of source.matchAll(/<(?:Sort)?Th\b([^>]*)>([\s\S]{0,120}?)<\/(?:Sort)?Th>/g)) {
        // A heading nobody sees has no width to fail: an actions column names
        // itself for a screen reader and draws icons.
        if (match[2].includes('sr-only')) continue
        const label = match[2].replace(/<[^>]*>/g, '').trim()
        if (label.length < 5 || label.includes('{')) continue
        const width = widthAt(`<Th${match[1]}>`, 'xl')
        if (typeof width === 'number' && width < 8) {
          cramped.push(`${file}: ${label} at ${width}%`)
        }
      }
    }
    expect(cramped).toEqual([])
  })
})

describe('where a table heading pins', () => {
  it('does not pin at all until something says so', () => {
    // The deck is read compiled rather than as source (`?inline`): the Tailwind
    // plugin owns this file, so a `?raw` import of it comes back empty. A custom
    // property survives compilation unchanged, which is all this needs.
    expect(deck).toMatch(/--table-heading-position:\s*relative/)
    expect(deck).toMatch(/--table-sticky-top:\s*0px/)
    // `Th` reads both rather than carrying `sticky` itself, which is what makes
    // pinning a decision about one table instead of about every table.
    expect(primitives).not.toMatch(/label sticky top-/)
    expect(primitives).toMatch(/\[position:var\(--table-heading-position\)\]/)
  })

  it('stops pinning on a box that scrolls', () => {
    // `Table` grows a scroll container once a column is dragged wider, and a
    // `Sheet` body is one always. Inside either, an offset is a push, not a pin.
    expect(primitives).toMatch(/overflow-x-auto \[--table-heading-position:relative\]/)
    expect(primitives).toMatch(/overflow-y-auto[^`'"]*\[--table-heading-position:relative\]/)
  })

  it('is opted into only by a surface that clips without scrolling', () => {
    const optIns = [
      ...auditTrail.matchAll(/className="([^"]*--table-heading-position:sticky[^"]*)"/g),
    ]
    expect(optIns.length).toBe(1)
    expect(optIns[0][1]).toContain('--table-sticky-top:var(--deck-header-h)')
    expect(optIns[0][1]).toContain('overflow-clip')
    expect(optIns[0][1]).not.toContain('overflow-hidden')
  })
})
