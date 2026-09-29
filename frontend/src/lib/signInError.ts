import axios from 'axios'

/*
 * What a refused sign-in says, in the console's own words.
 *
 * Keyed on what the server answers — the status, and on the directory route
 * the named refusals the account sync raises — and never on putting the
 * server's string on screen. Two rules decide the table:
 *
 * - **The local form must not become a user-enumeration oracle.** The server
 *   answers an unknown username, a wrong password, a federated account and a
 *   machine account with the same 401 on purpose, so all four read as one
 *   message here too. A hint about signing in through a provider is the same
 *   sentence for every 401, so it tells nobody which case they hit.
 * - **A distinction the server does make is not flattened.** A disabled account
 *   is a 403 the server only gives once the password was right, so it can read
 *   as itself without saying anything to a guesser.
 *
 * Anything this table does not recognise gets a plain console sentence rather
 * than whatever the server wrote.
 */

export type SignInRoute = 'local' | 'directory'

/** What arrived from a failed sign-in request. `status` is absent when no response did. */
export interface SignInFailure {
  status?: number
  /** The server's `error`, read only to recognise a named refusal — never shown. */
  message?: string
}

export const SIGN_IN_TEXT = {
  credentials: 'That username and password did not match. Check both and try again.',
  credentialsProviderHint:
    'If your account comes from your organisation, sign in with it below instead.',
  disabled: 'This account is disabled. An administrator can re-enable it.',
  incomplete: 'Enter both a username and a password.',
  unavailable:
    'kubemg could not check your sign-in just now. Try again in a moment, and tell your administrator if it keeps happening.',
  unreachable: 'Cannot reach the kubemg server. Check your connection and try again.',
  generic: 'Sign-in did not succeed. Try again, or ask your administrator.',
  directoryCredentials:
    'The directory did not accept that username and password. Check both and try again.',
  directoryUnreachable:
    'kubemg could not reach the directory to check your password — this is not a wrong password. Try again shortly, and tell your administrator if it persists.',
  noAccount:
    'You were recognised, but there is no kubemg account for you yet and this sign-in does not create one. Ask an administrator to add you.',
  conflict:
    'You were recognised, but your username already belongs to a different kubemg account, so no session was opened. An administrator can resolve it.',
  unsafeUsername:
    "You were recognised, but the username your organisation sent contains ':' or a control character, which no kubemg account may have. An administrator has to change what the provider sends as the username.",
  providerOff: 'This sign-in option has been switched off. Use another way in, or ask your administrator.',
  accountRefused:
    'You were recognised, but kubemg did not open a session for this account. An administrator can say why.',
} as const

/*
 * The account sync's refusals, which the server tells apart only by their
 * sentinel text. A refusal renamed on the server falls through to the generic
 * sentence rather than to its own string.
 */
const NAMED_REFUSALS: Array<[(message: string) => boolean, string]> = [
  [(m) => m === 'no KubeMG account is provisioned for this identity', SIGN_IN_TEXT.noAccount],
  [(m) => m === 'this account is disabled', SIGN_IN_TEXT.disabled],
  [(m) => m === 'an account with this username already exists', SIGN_IN_TEXT.conflict],
  [(m) => m.startsWith('the identity provider asserted a username containing'), SIGN_IN_TEXT.unsafeUsername],
  [(m) => m === 'this identity provider is disabled', SIGN_IN_TEXT.providerOff],
]

function namedRefusal(message: string | undefined): string | null {
  if (!message) return null
  const hit = NAMED_REFUSALS.find(([matches]) => matches(message))
  return hit ? hit[1] : null
}

/** Reads a failed request into the two facts the table is keyed on. */
export function readSignInFailure(error: unknown): SignInFailure {
  if (!axios.isAxiosError(error)) return {}
  const data = error.response?.data as { error?: unknown } | undefined
  return {
    status: error.response?.status,
    message: typeof data?.error === 'string' ? data.error : undefined,
  }
}

/**
 * The sentence for a refused sign-in. `hasProviders` adds the provider hint to
 * the local form's credentials refusal, and only there.
 */
export function signInErrorText(
  failure: SignInFailure,
  route: SignInRoute,
  { hasProviders = false }: { hasProviders?: boolean } = {},
): string {
  const { status, message } = failure
  if (status === undefined) return SIGN_IN_TEXT.unreachable
  if (status === 400) return SIGN_IN_TEXT.incomplete

  if (route === 'local') {
    if (status === 401) {
      return hasProviders
        ? `${SIGN_IN_TEXT.credentials} ${SIGN_IN_TEXT.credentialsProviderHint}`
        : SIGN_IN_TEXT.credentials
    }
    // The login route answers 403 for one thing only: a disabled account whose
    // password was right.
    if (status === 403) return SIGN_IN_TEXT.disabled
  } else {
    if (status === 401) return SIGN_IN_TEXT.directoryCredentials
    if (status === 502) return SIGN_IN_TEXT.directoryUnreachable
    if (status === 403 || status === 409) return namedRefusal(message) ?? SIGN_IN_TEXT.accountRefused
  }

  if (status >= 500) return SIGN_IN_TEXT.unavailable
  return SIGN_IN_TEXT.generic
}

/**
 * The sentence for an interactive sign-in that came back refused. The server
 * writes most of these for a person already; only the account sync's named
 * refusals, which are raw sentinels, are put in the console's words.
 */
export function callbackErrorText(message: string): string {
  return namedRefusal(message) ?? message
}
