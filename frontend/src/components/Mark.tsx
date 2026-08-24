/**
 * The kubemg mark — "hex keeper".
 *
 * An eyed hexagon on the left is the control plane; three hexagons hang off it
 * on three short connections. It is asymmetric and directional on purpose:
 * access is granted on the left and reaches the clusters on the right, which is
 * the product in one glyph. It is never rotated, mirrored, or given a second
 * colour.
 *
 * It inherits its colour from `currentColor` and its size from the class it is
 * given, so a caller decides both. The strokes are sized in user units, so they
 * scale with the box rather than thinning out as it grows. The viewBox is
 * cropped to the drawing, so the caller's own padding is the clear space the
 * brand asks for rather than being baked in twice.
 *
 * `brand/kubemg-mark.svg` is the source; `public/favicon.svg` is the same
 * drawing on its ink tile.
 */
export function Mark({ className }: { className?: string }) {
  return (
    <svg viewBox="16.6 21 90.9 78" aria-hidden="true" className={className}>
      <g fill="none" stroke="currentColor" strokeWidth="9" strokeLinecap="round">
        <path d="M70 45 L92 33" />
        <path d="M74 60 L90 60" />
        <path d="M70 75 L92 87" />
      </g>
      <g fill="currentColor">
        <path d="M98 21 L107.5 26.5 L107.5 37.5 L98 43 L88.5 37.5 L88.5 26.5 Z" />
        <path d="M96 49 L105.5 54.5 L105.5 65.5 L96 71 L86.5 65.5 L86.5 54.5 Z" />
        <path d="M98 77 L107.5 82.5 L107.5 93.5 L98 99 L88.5 93.5 L88.5 82.5 Z" />
        <path
          fillRule="evenodd"
          d="M46 26 L75.4 43 L75.4 77 L46 94 L16.6 77 L16.6 43 Z M38 53 a3.5 3.5 0 0 1 3.5 3.5 v7 a3.5 3.5 0 0 1 -7 0 v-7 a3.5 3.5 0 0 1 3.5 -3.5 z M55 53 a3.5 3.5 0 0 1 3.5 3.5 v7 a3.5 3.5 0 0 1 -7 0 v-7 a3.5 3.5 0 0 1 3.5 -3.5 z"
        />
      </g>
    </svg>
  )
}

/**
 * The mark on its chip — the favicon, as a component.
 *
 * Lime is 1.25:1 on the light deck's surfaces, so a bare lime mark cannot be
 * seen there; `mark-chip` is ink on that deck and nothing on the dark one, where
 * the page is already ink. Every place the mark appears goes through this rather
 * than drawing `Mark` directly, so the mark is lime everywhere or nowhere.
 *
 * Everything inside the chip is a **percentage of the chip**, not an `em`: the
 * caller sizes the chip itself, and an `em` in here would resolve against the
 * surrounding type instead — which is how a 28px chip ends up with a 9px mark on
 * it. The corner is the icon file's, 28 of 128.
 *
 * The mark is drawn wider than the icon file's own 61%, because the chip here is
 * not always painted. Where it is not, its padding becomes dead space between
 * the mark and the wordmark, and the lockup ends up spaced differently on the
 * two decks. The icon file needs that padding — it is a 16px tile with a rounded
 * corner and a browser's tab strip around it — and at lockup sizes it is only
 * air.
 */
export function MarkChip({ className }: { className?: string }) {
  return (
    <span
      className={`grid place-items-center rounded-[22%] bg-mark-chip ${className ?? 'size-[1.62em]'}`}
    >
      <Mark className="w-[76%] text-accent-fill" />
    </span>
  )
}

/**
 * The horizontal lockup: the mark and the wordmark.
 *
 * The wordmark is **always** lowercase `kubemg` — never KubeMG, never Kube MG —
 * and it is one colour rather than the two-tone `Kube`+`MG` this replaced.
 * Splitting a six-letter wordmark across two tones made the product read as two
 * words, which is the one thing the brand kit forbids about it. It takes the
 * caller's `currentColor`, so it is the text colour of wherever it is placed.
 *
 * One rule carries the colour: **the mark is always lime and the wordmark is
 * always the text around it.** That is the favicon, at every size and on both
 * decks. Lime is 1.25:1 on the light deck's surfaces and cannot be a bare glyph
 * there, so the mark keeps its ink chip — which is what the favicon has always
 * been — and on the dark deck the page is already ink, so `mark-chip` is
 * nothing and the mark sits straight on the deck at 12.7:1.
 *
 * The wordmark is set in the interface face at the weight the lockup file uses,
 * tracked tight, so it is the same drawing whether it is rendered here or
 * exported from `brand/kubemg-lockup.svg`.
 */
export function Lockup({ className, markClass }: { className?: string; markClass?: string }) {
  return (
    /* The gap and the chip are both in `em`, so the lockup is one drawing at
       every size it is used rather than a different one per call site.

       The two are aligned on their centres, not on the wordmark's baseline. A
       baseline alignment needs the chip nudged back up by hand — the chip has no
       baseline of its own, so it hangs off the bottom of the line — and that
       nudge is a constant that only looks right at one size. Centring needs no
       correction, and the wordmark's own mass is near enough symmetric about the
       line box for the two to read as level: `kubemg` has ascenders and a
       descender and no capitals. */
    <span className={`flex items-center gap-[0.32em] ${className ?? ''}`}>
      <MarkChip className={`size-[1.45em] shrink-0 ${markClass ?? ''}`} />
      <span className="font-bold tracking-[-0.045em] lowercase">kubemg</span>
    </span>
  )
}
