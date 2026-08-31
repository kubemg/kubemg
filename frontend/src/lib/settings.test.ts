import { describe, expect, it } from 'vitest'
import { settingSource } from './settings'

describe('settingSource', () => {
  it('reports a stored override', () => {
    expect(settingSource('https://kubemg.internal', 'https://localhost:8443')).toBe('override')
    expect(settingSource(48, 24)).toBe('override')
  })

  it('reports the environment when nothing is stored', () => {
    // The distinction that matters: this value cannot be changed from the
    // settings page alone — it came from how the process was started.
    expect(settingSource('', 'https://localhost:8443')).toBe('environment')
    expect(settingSource(0, 24)).toBe('environment')
  })

  it('reports the build default when neither is set', () => {
    expect(settingSource('', '')).toBe('default')
    expect(settingSource(0, 0)).toBe('default')
    expect(settingSource(undefined, null)).toBe('default')
  })

  it('treats the API conventions for unset as unset', () => {
    // Empty string for the text settings and zero for the numeric ones is how
    // the whole settings surface says "nothing stored here".
    expect(settingSource('', 0)).toBe('default')
    expect(settingSource(false, false)).toBe('default')
  })
})
