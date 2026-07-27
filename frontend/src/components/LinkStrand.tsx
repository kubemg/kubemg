export type StrandState = 'live' | 'direct' | 'down' | 'idle'

/* The shape of the line carries the state, so the strand survives greyscale and
   a squint: a live link swells toward the KubeMG end, a fixed wire is flat and
   even, a broken one is severed in the middle, a waiting one barely registers.
   No dashes and no marquee — the form does the work, not the motion. */
const TRACK: Record<StrandState, string> = {
  live: 'linear-gradient(90deg, transparent, currentColor 30%, currentColor)',
  direct: 'linear-gradient(90deg, currentColor, currentColor)',
  down: 'linear-gradient(90deg, currentColor, transparent 40%, transparent 60%, currentColor)',
  idle: 'linear-gradient(90deg, transparent, currentColor 45%, currentColor 55%, transparent)',
}

const COLOR: Record<StrandState, string> = {
  live: 'text-ok',
  direct: 'text-muted',
  down: 'text-danger',
  idle: 'text-faint',
}

const OPACITY: Record<StrandState, string> = {
  live: 'strand-live',
  direct: 'opacity-40',
  down: 'opacity-75',
  idle: 'opacity-30',
}

const READING: Record<StrandState, string> = {
  live: 'Agent tunnel open — traffic flows cluster to KubeMG',
  direct: 'KubeMG dials this cluster directly',
  down: 'No link to this cluster',
  idle: 'Waiting for the cluster to dial in',
}

/**
 * LinkStrand is the deck's signature: one cluster's link to KubeMG, drawn as a
 * single continuous hairline. An open agent tunnel gathers toward the KubeMG
 * end — the direction the connection is actually made, since the cluster dials
 * out and KubeMG never dials in — and carries a soft glow that breathes rather
 * than a pulse that races across the row. A direct-mode cluster is a flat wire
 * with no traffic of its own; a broken link is severed in the middle; a cluster
 * that has not dialled in yet is a faint thread fading out at both ends.
 */
export function LinkStrand({
  state,
  size = 'md',
  className,
}: {
  state: StrandState
  size?: 'sm' | 'md' | 'lg'
  className?: string
}) {
  const height = size === 'lg' ? 'h-1' : size === 'sm' ? 'h-px' : 'h-[3px]'

  return (
    <span
      role="img"
      aria-label={READING[state]}
      title={READING[state]}
      className={`relative block rounded-full ${height} ${COLOR[state]} ${className ?? ''}`}
    >
      {/* drop-shadow rather than box-shadow: it follows the gradient's own alpha,
          so the glow gathers where the link is live and dies out where it isn't. */}
      <span
        aria-hidden="true"
        className={`absolute inset-0 rounded-full ${OPACITY[state]}`}
        style={{
          backgroundImage: TRACK[state],
          filter: state === 'live' ? 'drop-shadow(0 0 3px currentColor)' : undefined,
        }}
      />
    </span>
  )
}

/**
 * StrandNode is an endpoint on a strand — a cluster, KubeMG itself, or the
 * caller. Used where the whole path is worth drawing rather than just the link.
 */
export function StrandNode({
  label,
  value,
  tone = 'idle',
}: {
  label: string
  value: string
  tone?: 'ok' | 'idle' | 'accent'
}) {
  const dot = tone === 'ok' ? 'bg-ok' : tone === 'accent' ? 'bg-accent' : 'bg-faint'

  return (
    <span className="flex min-w-0 flex-col gap-1">
      <span className="flex items-center gap-1.5">
        <span aria-hidden="true" className={`size-1.5 shrink-0 rounded-full ${dot}`} />
        <span className="label">{label}</span>
      </span>
      <span className="truncate font-mono text-[13px] text-fg" title={value}>
        {value}
      </span>
    </span>
  )
}
