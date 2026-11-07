import { describe, expect, it } from 'vitest'

import { DEFAULT_DOCS_BASE, docsHref } from './docs'

/*
 * The manual is MkDocs with directory URLs: `a/b.md` is served at `a/b/`, and
 * an index page at its directory. Whether a target exists is the docs build's
 * to check (hack/mkdocs_console_links.py); this is only the address.
 */
describe('docsHref', () => {
  it('serves a page as its directory, with the heading as the fragment', () => {
    expect(docsHref(DEFAULT_DOCS_BASE, 'install/tls.md#renewal')).toBe(
      'https://kubemg.readthedocs.io/en/latest/install/tls/#renewal',
    )
    expect(docsHref(DEFAULT_DOCS_BASE, 'access/mfa.md')).toBe('https://kubemg.readthedocs.io/en/latest/access/mfa/')
  })

  it('serves an index page at its directory, and the root index at the root', () => {
    expect(docsHref(DEFAULT_DOCS_BASE, 'install/index.md')).toBe('https://kubemg.readthedocs.io/en/latest/install/')
    expect(docsHref(DEFAULT_DOCS_BASE, 'index.md')).toBe('https://kubemg.readthedocs.io/en/latest/')
  })

  it('takes the server’s base with or without its trailing slash', () => {
    expect(docsHref('https://docs.example/en/v1.2.3', 'audit/trail.md')).toBe('https://docs.example/en/v1.2.3/audit/trail/')
  })
})
