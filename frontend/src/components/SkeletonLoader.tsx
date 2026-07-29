import { Row, Table, Td, Th } from './primitives'

/**
 * Skeletons: the shape of an answer that has not arrived yet.
 *
 * Every list, meter and chart in KubeMG is a live read down an agent tunnel to
 * somebody else's cluster, so the wait is real — hundreds of milliseconds on a
 * good day, longer over a slow link. A spinner in the middle of an empty panel
 * spends that wait saying nothing, and then the layout jumps when the rows
 * arrive. A skeleton spends it saying what is coming: this many columns, rows
 * this tall, a meter here and a meter there.
 *
 * They are deliberately plain — `--deck-raised` blocks with one travelling
 * highlight, the same fill a meter's track uses. A skeleton is scaffolding, and
 * scaffolding that draws attention to itself is worse than none: the eye should
 * land where the data is about to be, not on the animation.
 *
 * Rule of use: a skeleton stands in for data that has *never* been shown. Data
 * that is on screen and being refreshed stays on screen — replacing a table an
 * operator is reading with its own outline is a regression, not a loading state.
 */

/** A single placeholder block. Everything below is built from this. */
export function SkeletonBlock({
  className,
  width,
}: {
  className?: string
  /** An inline width, for the varied row widths a real list has. */
  width?: string
}) {
  return <span aria-hidden="true" className={`skeleton block ${className ?? ''}`} style={{ width }} />
}

/**
 * The widths a line of placeholder text cycles through. They are a fixed cycle
 * rather than random so a re-render does not reshuffle the skeleton — movement
 * that is not progress reads as a glitch.
 */
const LINE_WIDTHS = ['82%', '64%', '91%', '57%', '74%', '88%', '61%', '79%']

function lineWidth(index: number): string {
  return LINE_WIDTHS[index % LINE_WIDTHS.length]
}

/** A few lines of placeholder prose or values. */
export function SkeletonText({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div className={`flex flex-col gap-2 ${className ?? ''}`} role="presentation">
      {Array.from({ length: lines }, (_, index) => (
        <SkeletonBlock key={index} className="h-3" width={lineWidth(index)} />
      ))}
    </div>
  )
}

/**
 * A list that has not loaded, drawn as the table it is about to be. It uses the
 * same table atoms as the real lists, so the row height, the padding and the
 * hairlines are not an approximation of the thing being waited for — they are
 * the thing.
 */
export function TableSkeleton({
  columns = 4,
  rows = 6,
  label = 'Reading the cluster',
}: {
  columns?: number
  rows?: number
  /** What is being waited for, announced rather than drawn. */
  label?: string
}) {
  return (
    <div role="status" aria-busy="true">
      <span className="sr-only">{label}…</span>
      <Table>
        <thead>
          <tr>
            {Array.from({ length: columns }, (_, column) => (
              <Th key={column}>
                <SkeletonBlock className="h-2.5" width={column === 0 ? '48%' : '38%'} />
              </Th>
            ))}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rows }, (_, row) => (
            <Row key={row}>
              {Array.from({ length: columns }, (_, column) => (
                <Td key={column}>
                  {/* The first column is a name and reads longest; the rest are
                      short values, which is what a real row looks like. */}
                  <SkeletonBlock
                    className="h-3.5"
                    width={column === 0 ? lineWidth(row) : lineWidth(row + column + 3)}
                  />
                </Td>
              ))}
            </Row>
          ))}
        </tbody>
      </Table>
    </div>
  )
}

/**
 * A card or panel body that has not loaded: a heading line, a couple of values
 * and a rule, at the size the real content occupies.
 */
export function CardSkeleton({
  lines = 3,
  className,
  label = 'Loading',
}: {
  lines?: number
  className?: string
  label?: string
}) {
  return (
    <div role="status" aria-busy="true" className={`card p-5 ${className ?? ''}`}>
      <span className="sr-only">{label}…</span>
      <div className="flex items-center gap-3">
        <SkeletonBlock className="h-5" width="220px" />
        <SkeletonBlock className="h-4 w-16 rounded-chip" />
        <SkeletonBlock className="ml-auto h-3 w-24" />
      </div>
      <div className="mt-5 border-t border-line-soft pt-4">
        <SkeletonText lines={lines} />
      </div>
    </div>
  )
}

/**
 * A utilisation reading that has not arrived. It keeps the meter's own
 * geometry — label row, then the 6px track — because a meter that appears and
 * pushes the rows under it down is the jump this exists to prevent.
 */
export function MeterSkeleton({ className }: { className?: string }) {
  return (
    <div className={`min-w-0 ${className ?? ''}`} role="presentation">
      <div className="flex items-baseline gap-2">
        <SkeletonBlock className="h-2.5" width="38%" />
        <SkeletonBlock className="ml-auto h-2.5 w-12" />
      </div>
      <SkeletonBlock className="mt-1.5 h-1.5 w-full rounded-full" />
      <SkeletonBlock className="mt-1 h-2 w-8" />
    </div>
  )
}

/** A grid of meters, which is how capacity is drawn everywhere it appears. */
export function MeterGridSkeleton({
  count = 2,
  label = 'Reading usage',
}: {
  count?: number
  label?: string
}) {
  return (
    <div role="status" aria-busy="true" className="grid gap-4 sm:grid-cols-2">
      <span className="sr-only">{label}…</span>
      {Array.from({ length: count }, (_, index) => (
        <MeterSkeleton key={index} />
      ))}
    </div>
  )
}
