/**
 * Where a settings value came from.
 *
 * The distinction is the half the console used to leave out. "24 hours" answers
 * what is in force; it does not answer whether that is the build's own default,
 * a boot-time environment variable somebody would have to redeploy to change,
 * or a runtime override stored in the database — which is exactly the question
 * an operator asks before editing a field, and the one that previously required
 * reading the Go source to answer.
 */
export type SettingSource = 'default' | 'environment' | 'override'

/**
 * Which of the three a value is coming from, given the views the settings API
 * already reports: `overrides` (what is stored), `defaults` (what the process
 * booted with), and `effective` (what the two resolve to).
 *
 * It is a derivation rather than a new field on the wire, because the server
 * already answers it — an override that is set *is* the value, and otherwise the
 * environment's is. Unset is the API's own convention rather than this
 * function's invention: an empty string for the text settings and a zero for the
 * numeric ones, both of which mean "nothing stored here" everywhere else in the
 * settings surface.
 */
export function settingSource(override: unknown, envDefault: unknown): SettingSource {
  if (isSet(override)) return 'override'
  if (isSet(envDefault)) return 'environment'
  return 'default'
}

function isSet(value: unknown): boolean {
  return value !== '' && value !== 0 && value !== undefined && value !== null && value !== false
}
