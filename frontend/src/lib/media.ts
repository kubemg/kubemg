import { useEffect, useState } from 'react'

/**
 * The viewport height below which the deck treats the screen as short: a 14"
 * laptop at its default scaling gives a browser 830–950 CSS pixels depending
 * on toolbars and full screen, a desk monitor well over a thousand. Height and
 * not width, because what runs out first on a laptop is the sidebar's vertical
 * room — the tree and the account card compete for it — and CSS pixels rather
 * than device ones, so a display scaled up to "larger text" counts as the
 * shorter screen it now is.
 */
export const SHORT_VIEWPORT = '(max-height: 959.98px)'

function matches(query: string): boolean {
  try {
    return window.matchMedia(query).matches
  } catch {
    // No matchMedia (an old engine, a test DOM): read as the roomy default.
    return false
  }
}

/** useMediaQuery follows one CSS media query, live, across resizes and zoom. */
export function useMediaQuery(query: string): boolean {
  const [hit, setHit] = useState(() => matches(query))

  useEffect(() => {
    let list: MediaQueryList
    try {
      list = window.matchMedia(query)
    } catch {
      return
    }
    const update = () => setHit(list.matches)
    update()
    list.addEventListener('change', update)
    return () => list.removeEventListener('change', update)
  }, [query])

  return hit
}
