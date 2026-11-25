import { describe, expect, it } from 'vitest'

/*
 * A page subtitle is read in a glance, so it stays a few words and the rest
 * lives in the manual (#367). Held here rather than remembered: a subtitle
 * grows one clause at a time, and no single change looks like the one that
 * made it a paragraph.
 */

const pages = import.meta.glob('./**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<
  string,
  string
>

/** Every literal `description="…"` passed to AppShell, by file. */
function subtitles(): { file: string; text: string }[] {
  const out: { file: string; text: string }[] = []
  for (const [file, source] of Object.entries(pages)) {
    if (file.endsWith('.test.tsx')) continue
    for (const match of source.matchAll(/<AppShell\b/g)) {
      const start = match.index ?? 0
      const end = source.indexOf('\n    >', start)
      const tag = source.slice(start, end === -1 ? undefined : end)
      const literal = /\bdescription="([^"]*)"/.exec(tag)
      if (literal) out.push({ file, text: literal[1] })
    }
  }
  return out
}

describe('page subtitles', () => {
  it('stay a few words', () => {
    const long = subtitles().filter(({ text }) => text.split(/\s+/).filter(Boolean).length > 8)
    expect(long).toEqual([])
  })

  it('are still found by this test', () => {
    // The guard above passes trivially if the pattern stopped matching.
    expect(subtitles().length).toBeGreaterThan(8)
  })
})
