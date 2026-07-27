export type StrandState = 'live' | 'direct' | 'down' | 'idle'

/* Dash pitch differs per state so the strand survives greyscale and a squint:
   traffic is a fine dash, a fixed wire is solid, a broken link is a long dash. */
const TRACK: Record<StrandState, string> = {
  live: 'repeating-linear-gradient(90deg, currentColor 0 3px, transparent 3px 7px)',
  direct: 'linear-gradient(90deg, currentColor, currentColor)',
  down: 'repeating-linear-gradient(90deg, currentColor 0 7px, transparent 7px 13px)',
  idle: 'repeating-linear-gradient(90deg, currentColor 0 2px, transparent 2px 8px)',
}

const COLOR: Record<StrandState, string> = {
  live: 'text-ok',
  direct: 'text-muted',
  down: 'text-danger',
  idle: 'text-faint',
}

const OPACITY: Record<StrandState, string> = {
  live: 'opacity-45',
  direct: 'opacity-45',
  down: 'opacity-80',
  idle: 'opacity-40',
}

const READING: Record<StrandState, string> = {
  live: 'Agent tunnel open — traffic flows cluster to KubeMG',
  direct: 'KubeMG dials this cluster directly',
  down: 'No link to this cluster',
  idle: 'Waiting for the cluster to dial in',
}

/**
 * LinkStrand is the deck's signature: one cluster's link to KubeMG, drawn as a
 * track. When an agent tunnel is open a pulse travels along it right to left,
 * the direction the connection is actually made — the cluster dials out, KubeMG
 * never dials in. A direct-mode cluster is a fixed wire with no traffic of its
 * own; a broken link is a long dash with a notch bitten out of the middle.
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
      className={`relative block overflow-hidden rounded-full ${height} ${COLOR[state]} ${className ?? ''}`}
    >
      <span
        aria-hidden="true"
        className={`absolute inset-0 ${OPACITY[state]}`}
        style={{ backgroundImage: TRACK[state] }}
      />

      {state === 'live' ? (
        <span
          aria-hidden="true"
          className="strand-pulse absolute inset-y-0 left-0 w-1/3"
          style={{
            backgroundImage: 'linear-gradient(90deg, transparent, currentColor, transparent)',
          }}
        />
      ) : null}

      {state === 'down' ? (
        <span
          aria-hidden="true"
          className="absolute top-0 bottom-0 left-1/2 w-2 -translate-x-1/2 bg-surface"
        />
      ) : null}
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
