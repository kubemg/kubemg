/**
 * What an alarm channel form sends for its extra headers.
 *
 * Header values are never read back — a header is as good a place for a token
 * as the secret field — so an edit form opens with an empty box over a stored
 * set it cannot show. That makes three intents, and the request has to keep
 * them apart: `undefined` keeps what is stored (the field is omitted), `''`
 * clears it, and anything else replaces the whole set.
 */
export function headersPayload(typed: string, clear: boolean): string | undefined {
  if (clear) return ''
  const value = typed.trim()
  return value === '' ? undefined : value
}

/** The hint under the field, naming what is stored without its values. */
export function headersHint(storedNames: readonly string[] | undefined, clear: boolean): string {
  const base =
    'Optional JSON object, for a SIEM\'s tenant id or an API version. Authorization and Content-Type are set by kubemg and cannot be overridden here.'
  if (!storedNames?.length) return base
  if (clear) return `The stored headers (${storedNames.join(', ')}) will be removed on save.`
  return `Sent now: ${storedNames.join(', ')}. Values are stored encrypted and never read back — leave this empty to keep them, or enter a JSON object to replace them all.`
}
