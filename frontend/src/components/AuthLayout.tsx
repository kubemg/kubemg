import type { ReactNode } from 'react'
import { Moon, Sun } from 'lucide-react'
import { Lockup } from './Mark'
import { EnvironmentBanner } from './EnvironmentBanner'
import { useBranding } from '../state/branding-context'
import { useTheme } from '../lib/theme'

/**
 * The frame of the pages a person meets on the way *in*: the sign-in, and the
 * approval a `kubemg login` sends the browser to. One frame, so that the walk
 * from the terminal through the sign-in to the approval reads as one flow
 * rather than a sign-in page that drops you into the full console for a single
 * question about a terminal.
 *
 * The left half carries the page's own words on the rail; the right half one
 * card, centred between a top row (the deck toggle) and a footnote.
 */
export function AuthLayout({
  headline,
  lede,
  footnote,
  cardTestId,
  wide = false,
  children,
}: {
  /** The rail's statement — short, two lines at most. */
  headline: ReactNode
  lede: ReactNode
  /** The line under the card. */
  footnote: ReactNode
  cardTestId?: string
  /** A card with more to show than a form: 480px rather than 400. */
  wide?: boolean
  children: ReactNode
}) {
  const { theme, toggle } = useTheme()
  return (
    /* The banner is above everything, and it is the reason the branding read
       happens outside the auth gate: an operator has to be able to tell a
       production console from a staging one *before* typing a password into
       it, not after. */
    <div className="flex min-h-svh flex-col">
      <EnvironmentBanner />
      <main className="grid flex-1 lg:grid-cols-[1.1fr_minmax(420px,0.9fr)]">
        <section className="relative hidden flex-col justify-between overflow-hidden bg-rail p-10 lg:flex">
          {/* Texture, not signal — a static field standing in for the fleet,
              and a soft accent glow anchoring the corner. Nothing here
              animates: the deck's one moving mark is the breath on a live
              link, and this page has no cluster to draw one for. */}
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-0"
            style={{
              backgroundImage: 'radial-gradient(circle, var(--deck-rail-border) 1px, transparent 1px)',
              backgroundSize: '22px 22px',
            }}
          />
          <div
            aria-hidden="true"
            className="pointer-events-none absolute -right-28 -bottom-28 size-[480px] rounded-full"
            style={{
              /* A haze of the accent itself, not `accent-soft`. That token is a
                 fill meant to sit under text, so as a gradient it reads as a
                 dirty olive smudge on both decks rather than as lime light. */
              backgroundImage:
                'radial-gradient(circle, color-mix(in oklab, var(--deck-accent-fill) 20%, transparent), transparent 70%)',
            }}
          />

          {/* h-9: the same height as the deck toggle opposite, so the two
              halves share a top rail. */}
          <div className="relative flex h-9 items-center gap-3">
            <Lockup className="text-[22px] text-rail-fg" />
            {/* Beside the lockup, never instead of it. A console that presents
                itself wholly as somebody else's product is one nobody can get
                support for — and the organisation's own mark is what makes it
                theirs to introduce. */}
            <OrganisationIdentity tone="rail" />
          </div>

          <div className="relative max-w-md">
            <h1 className="text-[34px] leading-[1.1] font-bold tracking-[-0.03em] text-rail-fg">{headline}</h1>
            <p className="mt-4 text-[14px] leading-relaxed text-rail-muted">{lede}</p>
          </div>

          <p className="relative text-[12px] text-rail-faint">kubemg · centralized Kubernetes access</p>
        </section>

        {/* The right half keeps the left half's three rails — a top row, the
            content, a data line at the foot — on one column, so the toggle
            sits on the card's right edge, the footnote on its left, and the
            card is centred between two things rather than floating in what was
            left over. The toggle is in the flow rather than fixed, so it can
            never sit on top of the environment banner. */}
        <section className="flex flex-col bg-bg p-6 lg:p-10">
          <div className={`mx-auto flex w-full flex-1 flex-col ${wide ? 'max-w-[480px]' : 'max-w-[400px]'}`}>
            <div className="flex h-9 items-center justify-end">
              <button
                type="button"
                onClick={toggle}
                title={theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
                className="grid size-9 place-items-center rounded-control border border-line bg-surface text-muted transition-colors hover:bg-raised hover:text-fg"
              >
                {theme === 'dark' ? (
                  <Sun aria-hidden="true" className="size-4" />
                ) : (
                  <Moon aria-hidden="true" className="size-4" />
                )}
                <span className="sr-only">
                  {theme === 'dark' ? 'Switch to the light deck' : 'Switch to the dark deck'}
                </span>
              </button>
            </div>

            <div className="flex flex-1 items-center py-8">
              <div data-testid={cardTestId} className="card lift w-full p-8">
                {/* Below `lg` the brand panel beside this card is gone, so the
                    card carries the lockup itself. */}
                <div className="mb-7 flex items-center gap-3 lg:hidden">
                  <Lockup className="text-[20px] text-fg" />
                  <OrganisationIdentity tone="page" />
                </div>
                {children}
              </div>
            </div>

            <p className="text-[12px] text-faint">{footnote}</p>
          </div>
        </section>
      </main>
    </div>
  )
}

/**
 * The organisation's own mark and name, beside the lockup.
 *
 * It draws nothing when neither is configured, which is the default: an install
 * that has not been branded looks exactly as it did. The divider is only ever
 * drawn between two things that are both there.
 */
function OrganisationIdentity({ tone }: { tone: 'rail' | 'page' }) {
  const { branding } = useBranding()
  const name = branding?.organisation_name?.trim()
  const mark = branding?.organisation_mark?.trim()
  if (!name && !mark) return null

  const rule = tone === 'rail' ? 'bg-rail-line' : 'bg-line'
  const text = tone === 'rail' ? 'text-rail-muted' : 'text-muted'

  return (
    <>
      <span aria-hidden="true" className={`h-5 w-px shrink-0 ${rule}`} />
      {mark ? <img src={mark} alt="" className="size-6 shrink-0 object-contain" /> : null}
      {name ? <span className={`min-w-0 truncate text-[13px] ${text}`}>{name}</span> : null}
    </>
  )
}
