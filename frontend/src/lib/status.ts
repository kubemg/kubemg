/**
 * The 3px state marker on the leading edge of a row, card, or ribbon bar. State
 * reads as form as well as colour, so it survives greyscale and a glance from
 * across the room.
 */
export const SPINE_TONE: Record<string, string> = {
  healthy: 'bg-ok',
  unhealthy: 'bg-danger',
  pending: 'bg-faint/40',
}
