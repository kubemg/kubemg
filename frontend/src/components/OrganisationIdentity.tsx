import { useBranding } from '../state/branding-context'

type Tone = 'rail' | 'page'

/**
 * The organisation's own mark and name, beside the `kubemg` lockup — never
 * instead of it. A console that presents itself wholly as somebody else's
 * product is one nobody can get support for.
 *
 * It is drawn where the lockup is: the sign-in page, the sidebar's brand row and
 * the mobile navigation's. It used to sit in the footer too, where it ran on
 * from the release number as one unexplained line; beside the lockup it reads
 * as what it is — whose installation this is.
 *
 * It draws nothing when neither is configured, which is the default: an install
 * that has not been branded looks exactly as it did. The divider is only ever
 * drawn between two things that are both there.
 */
export function OrganisationIdentity({ tone }: { tone: Tone }) {
  const { branding } = useBranding()
  return (
    <OrganisationIdentityView
      tone={tone}
      name={branding?.organisation_name ?? ''}
      mark={branding?.organisation_mark ?? ''}
    />
  )
}

/** The same identity from values in hand — the branding page previews a draft
    with it, so the preview cannot drift from what the console draws. */
export function OrganisationIdentityView({ tone, name, mark }: { tone: Tone; name: string; mark: string }) {
  const shownName = name.trim()
  const shownMark = mark.trim()
  if (!shownName && !shownMark) return null

  const rule = tone === 'rail' ? 'bg-rail-line' : 'bg-line'
  const text = tone === 'rail' ? 'text-rail-muted' : 'text-muted'

  return (
    <>
      <span aria-hidden="true" className={`h-5 w-px shrink-0 ${rule}`} />
      {shownMark ? <img src={shownMark} alt="" className="size-6 shrink-0 object-contain" /> : null}
      {/* Sixty characters do not fit a 240px panel; the whole name stays one
          hover away. */}
      {shownName ? (
        <span title={shownName} className={`min-w-0 truncate text-[13px] ${text}`}>
          {shownName}
        </span>
      ) : null}
    </>
  )
}
