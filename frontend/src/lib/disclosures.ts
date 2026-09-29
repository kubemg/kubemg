import { useEffect, useState } from 'react'

/*
 * Whether one card's "why this is like this" explanation is open, remembered
 * per signed-in identity across visits — the same browser-storage pattern as
 * `favorites.ts`: a preference nobody else needs to read, so it lives in
 * localStorage rather than behind a server setting and a round trip.
 *
 * Scoped per user rather than one flag per browser, so a shared machine that
 * changes hands never hands the next person somebody else's choice — a
 * stranger to this browser simply finds every card closed, the same thing a
 * first visit finds. There is nothing to migrate for that: an unscoped flag
 * was never written.
 */

const STORAGE_PREFIX = 'kubemg.disclosures.'

function storageKey(id: string, userId: number | string | null): string {
  return `${STORAGE_PREFIX}${userId ?? 'anonymous'}.${id}`
}

function read(key: string): boolean {
  try {
    // Anything other than exactly what an open disclosure writes reads as
    // closed — a stranger's key, a hand-edited value, storage that refuses to
    // answer at all. The state a card cannot reach on its own is the safe
    // default, the same rule `favorites.ts` reads a malformed entry by.
    return window.localStorage.getItem(key) === 'open'
  } catch {
    return false
  }
}

function write(key: string, open: boolean) {
  try {
    if (open) {
      window.localStorage.setItem(key, 'open')
    } else {
      // Closed is the default every key starts at, so there is nothing to
      // remember about it — removing the entry reads exactly like a card
      // that was never opened.
      window.localStorage.removeItem(key)
    }
  } catch {
    // Private browsing refuses storage; the choice still holds for this render.
  }
}

/**
 * useDisclosureState is one card's own open/closed flag: closed on first
 * visit, remembered from here on. `id` names the card and should be stable
 * and unique within the page it lives on — two cards sharing an id would
 * share a memory neither asked for. `userId` is whoever is signed in right
 * now, or `null` before that is known, which reads the same as closed.
 */
export function useDisclosureState(
  id: string,
  userId: number | string | null,
): [boolean, (open: boolean) => void] {
  const key = storageKey(id, userId)
  const [open, setOpen] = useState(() => read(key))

  // Re-read whenever the key itself changes — a different card, or a
  // different person signed in without a full page reload — rather than only
  // on first mount, since neither of those remounts this component.
  useEffect(() => {
    setOpen(read(key))
  }, [key])

  function set(next: boolean) {
    setOpen(next)
    write(key, next)
  }

  return [open, set]
}
