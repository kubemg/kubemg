import { useEffect, useRef, useState } from 'react'

/**
 * Live reads: keeping what is on screen true, without spending a cluster on a
 * page nobody is looking at.
 *
 * Everything this console draws is a point-in-time answer — a pod list, a
 * cluster's link, a node's usage — and every one of them goes stale the moment
 * it lands. Two places made that obvious. A cluster registered a minute ago is
 * an empty page until the agent dials in, and the only thing that told an
 * operator it had was a manual reload; and a resource list left open while a
 * rollout runs keeps showing the rollout that was, so the console taught people
 * to hit Refresh rather than to trust it.
 *
 * So reads re-run on their own. Three rules make that affordable rather than a
 * fleet-wide load multiplier, because a live read is not free: on a list it is a
 * tunnel round trip, an impersonated API call and an audit record.
 *
 *   - **Only while somebody is looking.** A hidden tab reads nothing at all, and
 *     a visible tab nobody has touched for `IDLE_AFTER` is treated the same way
 *     — a console left open on a second monitor is not a console being watched.
 *     Coming back is itself a reason to re-read, so returning to a tab shows
 *     current state rather than whatever it held when it lost focus.
 *   - **One cadence, and it is slow.** `LIVE_INTERVAL` matches how often
 *     metrics-server resamples, which is roughly the fastest anything here
 *     genuinely changes. Faster would spend round trips on numbers that have not
 *     moved.
 *   - **It can be turned off, once, for everything.** The preference is shared
 *     and remembered: an operator on a metered link or one who does not want
 *     every open tab writing audit rows pauses it and the whole console stops.
 *     Refresh keeps working, and keeps being honest — it skips both caches.
 *
 * What this deliberately is *not* is a watch. Kubernetes has one, the tunnel
 * carries it (`pkg/bastion/stream.go`), and a stream per open list per operator
 * is a very different cost model from a poll that stops when the tab is hidden.
 * A poll also degrades correctly: a tick that fails leaves the last answer on
 * screen, where a broken stream leaves a page that has quietly stopped updating.
 */

/** How often a live read re-runs. metrics-server's own resample period. */
export const LIVE_INTERVAL = 15_000

/**
 * How often the fleet list re-runs. It is faster than `LIVE_INTERVAL` because
 * it is the one live read that never touches a cluster — it is a query against
 * KubeMG's own database plus the tunnel registry, so no impersonated call and
 * no audit record — and because it is what tells an operator their new cluster
 * has attached.
 */
export const FLEET_INTERVAL = 10_000

/**
 * And how often it re-runs while a cluster is still waiting to dial in. The
 * wizard's own handshake step polls at 3s for exactly this reason; a cluster
 * registered from anywhere else deserves the same answer without the operator
 * reloading the page to get it.
 */
export const FLEET_PENDING_INTERVAL = 4_000

/**
 * How long a tab may go untouched before it stops counting as watched. Long
 * enough that reading a long list or writing in another window does not pause
 * the page under you, short enough that a console forgotten after lunch is not
 * still reading a production cluster an hour later.
 */
const IDLE_AFTER = 10 * 60_000

/** How often idleness is re-checked. The window is ten minutes; a minute's
 * granularity on the *edge* of it is invisible, and the poll's own tick asks
 * `attentive()` directly, so nothing hangs on this timer's resolution. */
const IDLE_CHECK = 60_000

const ACTIVITY_EVENTS = ['pointerdown', 'pointermove', 'keydown', 'wheel', 'touchstart'] as const

const attention = new Set<() => void>()
let lastActivity = Date.now()
let idle = false
let idleTimer = 0

function notifyAttention() {
  for (const listener of attention) listener()
}

/* Activity handlers run on every mouse move, so this must stay a variable
   assignment and nothing else. Waking from idle is the one case that notifies,
   and by then the moves have stopped arriving for ten minutes. */
function onActivity() {
  lastActivity = Date.now()
  if (idle) {
    idle = false
    notifyAttention()
  }
}

function checkIdle() {
  const stale = Date.now() - lastActivity >= IDLE_AFTER
  if (stale !== idle) {
    idle = stale
    notifyAttention()
  }
}

function startWatching() {
  for (const event of ACTIVITY_EVENTS) {
    window.addEventListener(event, onActivity, { passive: true })
  }
  window.addEventListener('focus', onActivity)
  document.addEventListener('visibilitychange', onVisibility)
  idleTimer = window.setInterval(checkIdle, IDLE_CHECK)
}

function stopWatching() {
  for (const event of ACTIVITY_EVENTS) {
    window.removeEventListener(event, onActivity)
  }
  window.removeEventListener('focus', onActivity)
  document.removeEventListener('visibilitychange', onVisibility)
  window.clearInterval(idleTimer)
}

function onVisibility() {
  // A tab brought back to the front counts as touched: the operator is looking
  // at it now, whatever they were doing while it was hidden.
  if (document.visibilityState === 'visible') lastActivity = Date.now()
  idle = false
  notifyAttention()
}

/**
 * attentive answers "is somebody looking at this tab right now?" — asked
 * directly by every tick rather than read off state, so the answer is never a
 * minute out of date at the moment it decides to spend a cluster read.
 */
export function attentive(): boolean {
  if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return false
  return Date.now() - lastActivity < IDLE_AFTER
}

/** subscribeAttention notifies when the answer above changes for a reason other
 * than time passing. The listeners are what own the DOM listeners and the idle
 * timer: with nobody subscribed, this module costs nothing. */
export function subscribeAttention(listener: () => void): () => void {
  if (attention.size === 0) startWatching()
  attention.add(listener)
  return () => {
    attention.delete(listener)
    if (attention.size === 0) stopWatching()
  }
}

/** useAttention is the same answer as state, for a caller that renders it. */
export function useAttention(): boolean {
  const [watched, setWatched] = useState(attentive)

  useEffect(() => {
    setWatched(attentive())
    return subscribeAttention(() => setWatched(attentive()))
  }, [])

  return watched
}

const STORAGE_KEY = 'kubemg.live'

const preference = new Set<() => void>()
let enabled = readPreference()

function readPreference(): boolean {
  try {
    // Live by default: a console that silently shows old state is the problem
    // this exists to fix, so being opted out has to be a choice somebody made.
    return window.localStorage.getItem(STORAGE_KEY) !== 'off'
  } catch {
    return true
  }
}

/** liveEnabled is the shared preference. One console, one answer — a page that
 * kept its own would leave the operator pausing updates surface by surface. */
export function liveEnabled(): boolean {
  return enabled
}

export function setLiveEnabled(next: boolean) {
  if (next === enabled) return
  enabled = next
  try {
    window.localStorage.setItem(STORAGE_KEY, next ? 'on' : 'off')
  } catch {
    // Private browsing refuses storage; the choice still holds for this session.
  }
  for (const listener of preference) listener()
}

/** useLive reads the preference and toggles it, for the control that draws it. */
export function useLive(): { live: boolean; setLive: (next: boolean) => void } {
  const [live, setState] = useState(enabled)

  useEffect(() => {
    setState(enabled)
    const listener = () => setState(enabled)
    preference.add(listener)
    return () => {
      preference.delete(listener)
    }
  }, [])

  return { live, setLive: setLiveEnabled }
}

/**
 * useLiveTick runs `run` on an interval while the preference is on and the tab
 * is being watched, and once immediately when a tab that was away comes back
 * with a full interval's worth of staleness behind it.
 *
 * Mounting counts as a run: every caller here already reads on mount through
 * its own effect, and a tick firing beside that first read would double it.
 */
export function useLiveTick(
  run: () => void | Promise<void>,
  { interval = LIVE_INTERVAL, enabled: on = true }: { interval?: number; enabled?: boolean } = {},
) {
  const { live } = useLive()
  const watched = useAttention()

  // The callback closes over whatever the page currently has selected and is
  // rebuilt every render; holding it in a ref keeps the timer keyed on the
  // cadence rather than restarting on every render.
  const runRef = useRef(run)
  useEffect(() => {
    runRef.current = run
  })

  const lastRun = useRef(Date.now())

  useEffect(() => {
    if (!on || !live || !watched) return

    const tick = () => {
      lastRun.current = Date.now()
      void runRef.current()
    }

    // Back from a hidden tab or a wake from idle: what is on screen is as old as
    // the absence, so it is re-read now rather than at the end of the next
    // interval.
    if (Date.now() - lastRun.current >= interval) tick()

    const timer = window.setInterval(() => {
      // The idle check runs once a minute, so this is what keeps a tick from
      // landing in the gap between going idle and being noticed.
      if (attentive()) tick()
    }, interval)

    return () => window.clearInterval(timer)
  }, [on, live, watched, interval])
}
