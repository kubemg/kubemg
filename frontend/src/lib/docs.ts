/**
 * Where the console points into the manual.
 *
 * A settings page says what a field does in a line and leaves the why, the
 * trade-offs and the edge cases to the manual, which already carries them at
 * length. So every link the console draws into it is named here, as the
 * manual's own source path and heading anchor — `install/tls.md#renewal` — and
 * nowhere else.
 *
 * Keeping them in one place is what lets them be checked: the docs build
 * (`make docs-build`, and Read the Docs) reads this file and fails on a page
 * that is gone or a heading that was renamed, so a link here cannot quietly
 * start landing on the top of the wrong page.
 */

/** A page of the manual by its source path under `docs/`, optionally with the
    anchor of one of its headings. */
export type DocTarget = `${string}.md` | `${string}.md#${string}`

/** The manual as served when the server has not said where it is yet. The
    server's `/api/v1/version` answer is the authority; this is only what a
    link reads as for the moment before it arrives. */
export const DEFAULT_DOCS_BASE = 'https://kubemg.readthedocs.io/en/latest/'

/**
 * The address of a target on the built manual. MkDocs serves `a/b.md` at
 * `a/b/` and `a/index.md` at `a/`, so the `.md` becomes a directory.
 */
export function docsHref(base: string, target: DocTarget): string {
  const [file, anchor] = target.split('#') as [string, string | undefined]
  const page = file === 'index.md' ? '' : file.replace(/(^|\/)index\.md$/, '$1').replace(/\.md$/, '/')
  const root = base.endsWith('/') ? base : `${base}/`
  return `${root}${page}${anchor ? `#${anchor}` : ''}`
}

/**
 * Every link the console draws into the manual. Grouped by the page that draws
 * it, so a settings page reads its own row rather than spelling a path.
 */
export const DOCS = {
  deployment: {
    page: 'reference/settings.md#deployment-posture-read-only-not-a-setting',
    checks: {
      tls: 'install/tls.md#the-three-ways-a-certificate-ends-up-in-force',
      'agent-ca': 'install/tls.md#agent-trust-the-agent_ca_bundle',
      recording: 'audit/session-recording.md#where-files-go',
      'recording-key': 'audit/session-recording.md#encryption-at-rest',
      'secret-key': 'install/database.md#credentials-encrypted-at-rest',
      'secret-key-previous': 'install/database.md#rotating-the-key',
      'signing-key': 'install/environment.md#auth-jwt-bootstrap',
      mfa: 'access/mfa.md#requiring-a-second-factor',
      'mfa-super-admins': 'access/mfa.md#requiring-a-second-factor',
      'mfa-reset-armed': 'access/mfa.md#break-glass-the-only-super-admin-lost-both-phone-and-codes',
    },
  },
} as const satisfies Record<string, Record<string, DocTarget | Record<string, DocTarget>>>
