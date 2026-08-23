import { useCallback, useEffect, useRef, useState } from 'react'
import { withFreshReads } from '../api/client'
import { LIVE_INTERVAL, useLiveTick } from './live'

/**
 * A small stale-while-revalidate cache for reads.
 *
 * Everything the console reads is a live call down an agent tunnel, and the
 * navigation it invites is exactly the navigation that repeats those calls: a
 * sidebar click to Services and straight back to Pods, a drawer opened over the
 * list it came from and closed again, a tab returned to. Each of those was a
 * fresh round trip, so the console felt slower the more you used it.
 *
 * The rules here are the same three the backend cache uses, because the two are
 * halves of one answer:
 *
 *   - **Fresh means fresh.** Inside `STALE_TIME` an answer is served straight
 *     from memory with no request at all. That is what makes going back
 *     instant.
 *   - **Stale is shown, then replaced.** Past that, up to `MAX_AGE`, the last
 *     answer is rendered immediately *and* re-read; the caller is told it is
 *     revalidating so it can say so. This is the "keep previous data" behaviour
 *     that stops a list an operator is already reading from being replaced by
 *     its own skeleton.
 *   - **A key is the whole question.** Cluster, resource, namespace — anything
 *     that changes the answer belongs in the key, because an entry is only ever
 *     served to the identical question. The identity is *not* in the key: this
 *     cache lives in one signed-in browser tab, and it is cleared on sign-out.
 *
 * Refresh is the escape hatch and it is honest: it skips this cache and sends
 * `Cache-Control: no-cache`, so it also skips the server's. An operator who
 * clicks Refresh is asking the cluster.
 *
 * A caller may also ask for the answer to stay true on its own (`live`), which
 * is the same read on `lib/live.ts`'s cadence — only while somebody is looking
 * at the tab. A live tick is deliberately invisible: it never draws a skeleton,
 * never blanks what is on screen when it fails, and does not even re-render when
 * the answer has not changed. The point of it is that an operator stops having
 * to wonder whether what they are reading is current, and a page that flickers
 * every fifteen seconds to prove it is refreshing has missed that entirely.
 *
 * This is deliberately not React Query or SWR. What is needed is one hook, and
 * the smallest of those libraries is larger than the terminal emulator this app
 * lazy-loads to keep out of the main bundle.
 */

/** How long an answer is served without asking again. */
export const STALE_TIME = 5_000

/**
 * How long a stale answer may still be shown while it is being re-read. Past
 * this the entry is dropped: a list from ten minutes ago is not a useful first
 * impression of a cluster, and drawing it would be a claim rather than a hint.
 */
const MAX_AGE = 60_000

/** How many answers to hold. Keys carry a namespace and a resource, so a long
 * session in a big fleet would otherwise accumulate without bound. */
const MAX_ENTRIES = 200

type Entry = { value: unknown; at: number }

const entries = new Map<string, Entry>()

function readEntry(key: string): { value: unknown; fresh: boolean } | null {
  const found = entries.get(key)
  if (!found) return null

  const age = Date.now() - found.at
  if (age > MAX_AGE) {
    entries.delete(key)
    return null
  }
  return { value: found.value, fresh: age <= STALE_TIME }
}

/** entryAge is how old the held answer is, or null if there is none. It is what
 * keeps a live tick from re-reading something that was just read by hand. */
function entryAge(key: string): number | null {
  const found = entries.get(key)
  return found ? Date.now() - found.at : null
}

/**
 * sameAnswer compares a new answer with the one on screen. Exported because the
 * fleet list does the same thing outside this cache, for the same reason. A live read mostly
 * returns exactly what it returned last time — nothing in the cluster moved —
 * and replacing state with an identical value re-renders a table, recomputes
 * every derived insight above it and resets nothing anybody asked to be reset.
 * Skipping that is most of what makes the refresh feel smooth.
 *
 * It is a structural compare rather than a shallow one because every answer here
 * is a decoded JSON list, which is exactly what this is cheap and correct for;
 * anything it cannot serialize is treated as changed, which is the safe way to
 * be wrong.
 */
export function sameAnswer(a: unknown, b: unknown): boolean {
  if (a === b) return true
  try {
    return JSON.stringify(a) === JSON.stringify(b)
  } catch {
    return false
  }
}

function writeEntry(key: string, value: unknown) {
  // Map preserves insertion order, so the oldest key is the first one out.
  if (entries.size >= MAX_ENTRIES && !entries.has(key)) {
    const oldest = entries.keys().next()
    if (!oldest.done) entries.delete(oldest.value)
  }
  entries.delete(key)
  entries.set(key, { value, at: Date.now() })
}

/**
 * Drop cached answers. With no prefix it clears everything, which is what
 * signing out does — the next person at this browser must not be shown the
 * previous one's cluster.
 */
export function invalidateQueries(prefix?: string) {
  if (prefix === undefined) {
    entries.clear()
    return
  }
  for (const key of entries.keys()) {
    if (key.startsWith(prefix)) entries.delete(key)
  }
}

/** queryKey joins parts into a key. Nullish parts are legal and read as empty. */
export function queryKey(...parts: (string | number | boolean | null | undefined)[]): string {
  return parts.map((part) => (part === null || part === undefined ? '' : String(part))).join('|')
}

export type CachedQuery<T> = {
  /** The answer, or null when there has never been one to show. */
  data: T | null
  /** Whatever the read threw, for the caller to turn into a message. */
  error: unknown
  /** True when there is nothing to draw yet — this is the skeleton's cue. */
  loading: boolean
  /** True when data is on screen and a newer answer is on its way. */
  revalidating: boolean
  /**
   * When the answer on screen was read, as epoch milliseconds. A live tick
   * updates it even when the answer came back identical — that a read happened
   * is the fact a surface saying "live" is claiming, and it is deliberately the
   * *only* state a tick changes in that case. There is no `polling` flag to go
   * with it: nothing here should spin, dim or disable itself four times a minute
   * for a read nobody asked for.
   */
  updatedAt: number | null
  /**
   * True when a live tick failed and what is drawn is older than it should be.
   * The data stays: one failed read is not a reason to replace a list somebody
   * is reading with an error. This is how a surface stays honest about it.
   */
  stale: boolean
  /** Re-read, skipping this cache and the server's. */
  refresh: () => Promise<void>
}

type State<T> = {
  key: string | null
  data: T | null
  error: unknown
  loading: boolean
  revalidating: boolean
  updatedAt: number | null
  stale: boolean
}

function idle<T>(): State<T> {
  return {
    key: null,
    data: null,
    error: null,
    loading: false,
    revalidating: false,
    updatedAt: null,
    stale: false,
  }
}

/**
 * useCachedQuery reads `key` through the cache above. A null key means there is
 * nothing to read yet — no cluster picked, no namespace resolved — and is not an
 * error; the hook simply sits idle, which is what lets a page render its chrome
 * before it knows what it is showing.
 *
 * `live` asks for the answer to keep itself true. See the note at the top of the
 * file, and `lib/live.ts` for when a tick is allowed to happen at all.
 */
export function useCachedQuery<T>(
  key: string | null,
  read: () => Promise<T>,
  { live = false, interval = LIVE_INTERVAL }: { live?: boolean; interval?: number } = {},
): CachedQuery<T> {
  const [state, setState] = useState<State<T>>(() => {
    if (key === null) return idle<T>()
    const cached = readEntry(key)
    const at = entryAge(key)
    const base = { key, stale: false, updatedAt: at === null ? null : Date.now() - at }
    if (cached?.fresh) {
      return { ...base, data: cached.value as T, error: null, loading: false, revalidating: false }
    }
    return {
      ...base,
      data: (cached?.value as T) ?? null,
      error: null,
      loading: cached === null,
      revalidating: cached !== null,
    }
  })

  // The read closes over whatever the page currently has selected, and it is
  // rebuilt on every render. Holding it in a ref is what keeps the fetch effect
  // keyed on the question — the key — rather than firing again because a closure
  // was recreated. This effect is declared first so it has already run by the
  // time the one below reads the ref.
  const readRef = useRef(read)
  useEffect(() => {
    readRef.current = read
  })

  // Tracks which key the newest in-flight read belongs to, so an answer that
  // arrives after the operator has moved on is dropped rather than rendered
  // under the wrong heading.
  const activeKey = useRef<string | null>(key)
  // And whether one is in flight at all, which is what stops a live tick from
  // asking the same question twice at once.
  const inFlight = useRef(false)

  const run = useCallback(async (target: string, fresh: boolean, background = false) => {
    activeKey.current = target
    inFlight.current = true
    try {
      const value = fresh ? await withFreshReads(() => readRef.current()) : await readRef.current()
      if (activeKey.current !== target) return
      writeEntry(target, value)
      setState((previous) => {
        // A live tick that changed nothing keeps the data it already has, so
        // every table, chart and derived reading above it is left exactly as it
        // was; only the timestamp moves, because a read did happen.
        const unchanged = background && previous.key === target && sameAnswer(previous.data, value)
        return {
          key: target,
          data: unchanged ? previous.data : value,
          error: null,
          loading: false,
          revalidating: false,
          updatedAt: Date.now(),
          stale: false,
        }
      })
    } catch (error) {
      if (activeKey.current !== target) return
      setState((previous) => {
        // A background tick keeps what is on screen. The operator did not ask
        // for this read, and answering their list with an error because one
        // fifteen-second poll failed would be the console losing its nerve; the
        // next tick says the same thing if it is real, and `stale` says so
        // meanwhile.
        if (background && previous.key === target && previous.data !== null) {
          return previous.stale ? previous : { ...previous, stale: true }
        }
        // A failed read drops what it was replacing: showing a list next to the
        // error explaining that it could not be read is a contradiction.
        return {
          key: target,
          data: null,
          error,
          loading: false,
          revalidating: false,
          updatedAt: null,
          stale: false,
        }
      })
    } finally {
      inFlight.current = false
    }
  }, [])

  useEffect(() => {
    if (key === null) {
      activeKey.current = null
      setState(idle<T>())
      return
    }

    const cached = readEntry(key)
    const age = entryAge(key)
    if (cached?.fresh) {
      // The whole point: a question asked again within the window is answered
      // without touching the network.
      activeKey.current = key
      setState({
        key,
        data: cached.value as T,
        error: null,
        loading: false,
        revalidating: false,
        updatedAt: age === null ? null : Date.now() - age,
        stale: false,
      })
      return
    }

    setState((previous) => ({
      key,
      // A stale entry for *this* key is worth showing while it is re-read. The
      // previous key's data is not: several resources share a table, and
      // drawing one list under another's heading is worse than a skeleton.
      data: (cached?.value as T) ?? null,
      error: null,
      loading: cached === null,
      revalidating: cached !== null || previous.key === key,
      updatedAt: cached === null || age === null ? null : Date.now() - age,
      stale: false,
    }))
    void run(key, false)

    return () => {
      // Whatever is in flight no longer belongs to what is on screen.
      if (activeKey.current === key) activeKey.current = null
    }
  }, [key, run])

  const refresh = useCallback(async () => {
    if (key === null) return
    entries.delete(key)
    setState((previous) => ({
      ...previous,
      key,
      error: null,
      revalidating: previous.data !== null,
      stale: false,
    }))
    await run(key, true)
  }, [key, run])

  /*
   * The live tick.
   *
   * It stands down for a read already in flight and for an answer somebody just
   * asked for by hand — a tick landing on top of a manual Refresh would spend a
   * second cluster read to learn what the first one is about to say.
   *
   * And it does *not* ask fresh, unlike Refresh. A tick is allowed to be
   * answered by the server's own five-second cache: at this cadence that is at
   * most a third of an interval's staleness, and in exchange several tabs of the
   * same console — a list, the drawer over it, the same page open twice —
   * collapse into one tunnel round trip and one audit record instead of one
   * each. Refresh is what asks the cluster, and it stays that way.
   */
  useLiveTick(
    useCallback(() => {
      if (key === null || inFlight.current) return
      const age = entryAge(key)
      if (age !== null && age < interval / 2) return
      return run(key, false, true)
    }, [key, interval, run]),
    { enabled: live && key !== null, interval },
  )

  return {
    data: state.key === key ? state.data : null,
    error: state.key === key ? state.error : null,
    loading: state.key === key ? state.loading : key !== null,
    revalidating: state.key === key ? state.revalidating : false,
    updatedAt: state.key === key ? state.updatedAt : null,
    stale: state.key === key ? state.stale : false,
    refresh,
  }
}
