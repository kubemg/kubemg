import { BANNER_TONE_CLASS, bannerTone, hasBanner } from '../lib/branding'
import { useBranding } from '../state/branding-context'

/**
 * The strip an operator reads before they act: which installation this is.
 *
 * It draws nothing at all unless an administrator has written one. That is the
 * point rather than a courtesy — a console that always carries a banner is one
 * where the banner stops being read, and the whole value of this line is that
 * its presence means something. Most installs will never set it; the ones that
 * do are the ones where two consoles look alike and only one of them matters.
 *
 * It is `role="status"` rather than `role="alert"`: a banner is a standing fact
 * about the page, not an event that just happened, and an alert would be
 * announced over whatever a screen reader was in the middle of on every
 * navigation.
 *
 * It is not dismissable. A banner somebody can close is one that is closed on
 * the day it matters, and it costs a single line of a page that has scrolling
 * content beneath it anyway.
 */
export function EnvironmentBanner() {
  const { branding } = useBranding()
  if (!hasBanner(branding)) return null

  return (
    <div
      role="status"
      className={`flex items-center justify-center gap-2 border-b px-4 py-1.5 text-center text-[12.5px] font-medium tracking-[0.02em] ${
        BANNER_TONE_CLASS[bannerTone(branding)]
      }`}
    >
      <span className="min-w-0 truncate">{branding.banner_text}</span>
    </div>
  )
}
