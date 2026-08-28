import { describe, expect, it } from 'vitest'

// The file is read as text rather than through Node's `fs`, which this project
// carries no types for: `?raw` is Vite's own way of asking for a module's source
// and it works the same under vitest.
import source from './ResourceTables.tsx?raw'

/*
 * A column width is the one place in this console where a valid-looking CSS
 * value is thrown away in silence. Chrome's fixed table layout discards a track
 * width that is a math function mixing a percentage with an absolute length, so
 * `w-[min(16%,11rem)]` is not a narrower `16%` — it is `auto`, and a table full
 * of them renders at widths nobody chose. It costs nothing at review time and it
 * is invisible in a screenshot until a heading ellipsises, which is why the rule
 * is asserted here instead of being remembered.
 */

/** Every Tailwind arbitrary width in the file, breakpoint prefix included. */
function widths(): string[] {
  return source.match(/\b(?:[a-z]+:)?w-\[[^\]]*\]/g) ?? []
}

describe('resource table column widths', () => {
  it('never puts a percentage inside a math function', () => {
    const offenders = widths().filter(
      (w) => /\b(?:min|max|clamp)\(/.test(w) && w.includes('%'),
    )
    expect(offenders).toEqual([])
  })

  it('still sizes its columns', () => {
    // The guard above passes trivially if the widths were deleted rather than
    // fixed, so hold the file to having real percentage columns in it.
    expect(widths().filter((w) => /w-\[\d+%\]$/.test(w)).length).toBeGreaterThan(50)
  })
})
