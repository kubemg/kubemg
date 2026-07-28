/**
 * The KubeMG mark: three strands gathering into one node, which is the product
 * in one glyph — every cluster dials out and everything converges here. It is
 * the same drawing as `public/favicon.svg`, and it is a component rather than
 * three copies of a path because it had become three copies of a path.
 *
 * It inherits its colour from `currentColor` and its size from the class it is
 * given, so a caller decides both. The strokes are sized in user units, so they
 * scale with the box rather than thinning out as it grows.
 */
export function Mark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 20 20"
      aria-hidden="true"
      className={className}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
    >
      <path d="M2.5 5.5h5.5M2.5 10h9.5M2.5 14.5h5.5" />
      <circle cx="16" cy="10" r="2.1" fill="currentColor" stroke="none" />
    </svg>
  )
}
