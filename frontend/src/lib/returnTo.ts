/**
 * Where a signed-out visitor was going, so the sign-in can take them there.
 *
 * It exists for one link above all: the `kubemg login` URL a CLI opens in the
 * browser, which is useless if signing in drops the person on the fleet
 * overview with the code gone. Kept in sessionStorage — per tab, gone with it —
 * and only ever a path on this origin: anything that is not `/something` (an
 * absolute URL, a protocol-relative `//host`) is refused, so this can never be
 * turned into a redirect off the console.
 */
const KEY = 'kubemg.returnTo'

export function isSafeReturnPath(path: string): boolean {
  return path.startsWith('/') && !path.startsWith('//') && !path.startsWith('/\\') && path !== '/login'
}

export function rememberReturnTo(path: string): void {
  if (!isSafeReturnPath(path)) return
  try {
    sessionStorage.setItem(KEY, path)
  } catch {
    // Storage refused (private mode, blocked site data): the sign-in lands on
    // the overview, which is where it went before this existed.
  }
}

export function returnTo(): string {
  try {
    const path = sessionStorage.getItem(KEY)
    return path && isSafeReturnPath(path) ? path : '/'
  } catch {
    return '/'
  }
}

export function forgetReturnTo(): void {
  try {
    sessionStorage.removeItem(KEY)
  } catch {
    // Nothing to forget.
  }
}
