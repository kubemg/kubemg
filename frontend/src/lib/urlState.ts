import { useCallback, useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router'

/*
 * A page's filters, kept in the address.
 *
 * A filter held in component state is lost on reload and cannot be pasted
 * into a ticket; the same filter in the query string is a link to exactly what
 * somebody was looking at. Each write replaces the history entry rather than
 * pushing one, so narrowing a list five times does not cost five Backs.
 *
 * Writes read the *current* address rather than the one this render saw, so
 * two writes in one handler (clearing two filters at once) compose instead of
 * the second undoing the first.
 */

type Update<T> = T | ((current: T) => T)

function current(): URLSearchParams {
  return new URLSearchParams(window.location.search)
}

/** Apply one change to the query string, against the address as it is now. */
export function useWriteParam(): (key: string, value: string | null) => void {
  const [, setSearchParams] = useSearchParams()
  // The router hands out a new setter whenever the address changes; held in a
  // ref, every setter built on this one keeps one identity for the life of the
  // page, so an effect can depend on it without re-running on every keystroke.
  const latest = useRef(setSearchParams)
  useEffect(() => {
    latest.current = setSearchParams
  })
  return useCallback((key: string, value: string | null) => {
    const next = current()
    if (value === null || value === '') next.delete(key)
    else next.set(key, value)
    latest.current(next, { replace: true })
  }, [])
}

/** One text value in the address; the fallback is what an absent key means. */
export function useUrlText(key: string, fallback = ''): [string, (next: Update<string>) => void] {
  const [params] = useSearchParams()
  const write = useWriteParam()
  const value = params.get(key) ?? fallback
  const set = useCallback(
    (next: Update<string>) => {
      const was = current().get(key) ?? fallback
      const resolved = typeof next === 'function' ? next(was) : next
      write(key, resolved === fallback ? null : resolved)
    },
    [key, fallback, write],
  )
  return [value, set]
}

/** A flag in the address: present as `true`, absent otherwise. */
export function useUrlFlag(key: string): [boolean, (next: Update<boolean>) => void] {
  const [params] = useSearchParams()
  const write = useWriteParam()
  const value = params.get(key) === 'true'
  const set = useCallback(
    (next: Update<boolean>) => {
      const was = current().get(key) === 'true'
      const resolved = typeof next === 'function' ? next(was) : next
      write(key, resolved ? 'true' : null)
    },
    [key, write],
  )
  return [value, set]
}

/** A set in the address, comma-separated. */
export function useUrlList(key: string): [string[], (next: Update<string[]>) => void] {
  const [params] = useSearchParams()
  const write = useWriteParam()
  const raw = params.get(key) ?? ''
  const value = raw === '' ? EMPTY : raw.split(',')
  const set = useCallback(
    (next: Update<string[]>) => {
      const wasRaw = current().get(key) ?? ''
      const was = wasRaw === '' ? [] : wasRaw.split(',')
      const resolved = typeof next === 'function' ? next(was) : next
      write(key, resolved.length === 0 ? null : resolved.join(','))
    },
    [key, write],
  )
  return [value, set]
}

const EMPTY: string[] = []
