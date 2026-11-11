/**
 * What ties a federated sign-in to the browser tab that started it.
 *
 * The session comes back from the identity provider in the callback's URL
 * fragment. Nothing in that URL said which browser had asked for it, so a
 * callback somebody else started (their own sign-in, completed at their IdP and
 * never followed) or a bare `#token=` link could be sent to someone, and opening
 * it signed them into the sender's account: what they did next was done as the
 * sender. That is a login CSRF.
 *
 * So the login page sends a random value with the sign-in, kept in this tab's
 * sessionStorage, and the server hands it back beside the session. The callback
 * takes the session only when the value it brings is the one this tab holds,
 * and spends it. It is not a secret from the person signing in; it only has to
 * be one a third party cannot have put in this tab.
 */
const KEY = 'kubemg.ssoBinding'

function freshBinding(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  let text = ''
  for (const byte of bytes) text += String.fromCharCode(byte)
  return btoa(text).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * This tab's binding, created on first use. Every provider button on the page
 * carries the same one, since the URLs are drawn before anyone clicks. An empty
 * string means the browser refused storage, and the callback will refuse too.
 */
export function ssoBinding(): string {
  try {
    const existing = sessionStorage.getItem(KEY)
    if (existing) return existing
    const binding = freshBinding()
    sessionStorage.setItem(KEY, binding)
    return binding
  } catch {
    return ''
  }
}

/**
 * Whether a returned binding is this tab's, spending it either way: a binding
 * answers one callback, and the next sign-in from this tab draws a new one.
 */
export function takeSSOBinding(returned: string | null): boolean {
  let held: string | null = null
  try {
    held = sessionStorage.getItem(KEY)
    sessionStorage.removeItem(KEY)
  } catch {
    return false
  }
  return Boolean(held) && Boolean(returned) && held === returned
}
