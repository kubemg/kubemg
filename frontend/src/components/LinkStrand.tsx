export type StrandState = 'live' | 'direct' | 'down' | 'idle'

/* The shape of the trace carries the state, so the strand survives greyscale
   and a squint: a live link has a pulse gathered toward the KubeMG end — the
   direction the connection is actually made — a fixed wire is a flat run, a
   broken one is cut clean in the middle, and a waiting one is a line not yet
   drawn solid. It reads as telemetry, not as a gauge filling up. */
const PATH: Record<StrandState, string> = {
  live: 'M0 12 L60 12 L65 3 L70 21 L75 6 L80 17 L84 12 L100 12',
  direct: 'M0 12 L100 12',
  down: 'M0 12 L42 12 M42 5 L42 19 M58 5 L58 19 M58 12 L100 12',
  idle: 'M0 12 L100 12',
}

const DASH: Record<StrandState, string | undefined> = {
  live: undefined,
  direct: undefined,
  down: undefined,
  idle: '1.5 5',
}

const COLOR: Record<StrandState, string> = {
  live: 'text-ok',
  direct: 'text-muted',
  down: 'text-danger',
  idle: 'text-faint',
}

const OPACITY: Record<StrandState, string> = {
  live: 'strand-live',
  direct: 'opacity-55',
  down: 'opacity-85',
  idle: 'opacity-45',
}

const READING: Record<StrandState, string> = {
  live: 'Agent tunnel open — traffic flows cluster to KubeMG',
  direct: 'KubeMG dials this cluster directly',
  down: 'No link to this cluster',
  idle: 'Waiting for the cluster to dial in',
}

/**
 * LinkStrand is the deck's signature: one cluster's link to KubeMG, drawn as a
 * telemetry trace rather than a gauge. An open agent tunnel carries a pulse
 * gathered toward the KubeMG end, glowing with a soft breath rather than a
 * pulse that races across the row. A direct-mode cluster is a flat, steady
 * run with no traffic of its own; a broken link is cut clean in the middle;
 * a cluster that has not dialled in yet is a line not yet drawn solid.
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
  const height = size === 'lg' ? 'h-4' : size === 'sm' ? 'h-2' : 'h-2.5'

  return (
    <span
      role="img"
      aria-label={READING[state]}
      title={READING[state]}
      className={`relative block ${height} ${COLOR[state]} ${className ?? ''}`}
    >
      {/* drop-shadow rather than box-shadow: it follows the stroke's own alpha,
          so the glow only ever gathers around a trace that is actually live. */}
      <svg
        aria-hidden="true"
        viewBox="0 0 100 24"
        preserveAspectRatio="none"
        className={`absolute inset-0 h-full w-full ${OPACITY[state]}`}
        style={{
          filter: state === 'live' ? 'drop-shadow(0 0 2.5px currentColor)' : undefined,
        }}
      >
        <path
          d={PATH[state]}
          fill="none"
          stroke="currentColor"
          strokeWidth={3}
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeDasharray={DASH[state]}
        />
      </svg>
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
