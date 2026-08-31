import { describe, expect, it } from 'vitest'
import type { Branding } from '../api/types'
import {
  MAX_SHORT_NAME,
  bannerTone,
  deriveChip,
  hasBanner,
  normalizeShortName,
  railChip,
} from './branding'

describe('railChip', () => {
  it('prefers the stored short name', () => {
    expect(railChip({ name: 'prod-eu-west-1', short_name: 'EU1' })).toBe('EU1')
  })

  it('raises a stored name that was written lower case', () => {
    expect(railChip({ name: 'prod-eu-west-1', short_name: 'eu1' })).toBe('EU1')
  })

  it('falls back to the derivation when none is stored', () => {
    // A fleet registered before short names existed has none, and must look
    // exactly as it did rather than showing a column of blanks.
    expect(railChip({ name: 'prod-eu-west-1' })).toBe('PEW')
    expect(railChip({ name: 'prod-eu-west-1', short_name: '' })).toBe('PEW')
    expect(railChip({ name: 'prod-eu-west-1', short_name: '   ' })).toBe('PEW')
  })

  it('is why the short name exists: the derivation collides at fleet scale', () => {
    // This is the defect the field was added for, asserted so nobody "fixes"
    // the fallback into being the whole answer again.
    expect(deriveChip('prod-eu-west-1')).toBe(deriveChip('prod-eu-west-2'))
    expect(railChip({ name: 'prod-eu-west-1', short_name: 'EU1' })).not.toBe(
      railChip({ name: 'prod-eu-west-2', short_name: 'EU2' }),
    )
  })
})

describe('deriveChip', () => {
  it('takes initials across a separated name', () => {
    expect(deriveChip('minikube-direct-e2e')).toBe('MDE')
  })

  it('takes the first three characters of a single word', () => {
    expect(deriveChip('LocalKube')).toBe('LOC')
  })

  it('stops at three parts', () => {
    expect(deriveChip('a-b-c-d-e')).toBe('ABC')
  })

  it('answers something for a name with nothing alphanumeric in it', () => {
    expect(deriveChip('***')).toBe('***')
  })
})

describe('normalizeShortName', () => {
  it('mirrors the server: upper case, alphanumeric, bounded', () => {
    expect(normalizeShortName('eu-west-1')).toBe('EUWE')
    expect(normalizeShortName('eu1')).toBe('EU1')
    expect(normalizeShortName('production')).toHaveLength(MAX_SHORT_NAME)
  })

  it('leaves nothing rather than inventing something', () => {
    expect(normalizeShortName('—/—')).toBe('')
    expect(normalizeShortName('')).toBe('')
  })
})

describe('hasBanner', () => {
  it('is false for nothing configured', () => {
    expect(hasBanner(null)).toBe(false)
    expect(hasBanner({})).toBe(false)
  })

  it('is false for whitespace, which is not a banner', () => {
    expect(hasBanner({ banner_text: '   ' })).toBe(false)
  })

  it('is true for text, tone or no tone', () => {
    expect(hasBanner({ banner_text: 'PRODUCTION' })).toBe(true)
  })

  it('is false for a tone with no text', () => {
    // A tone alone is not a banner. The server refuses to report one; this is
    // the console agreeing rather than trusting.
    const toneOnly: Branding = { banner_tone: 'critical' }
    expect(hasBanner(toneOnly)).toBe(false)
  })
})

describe('bannerTone', () => {
  it('defaults to neutral', () => {
    expect(bannerTone(null)).toBe('neutral')
    expect(bannerTone({ banner_text: 'PRODUCTION' })).toBe('neutral')
  })

  it('keeps the two loud tones', () => {
    expect(bannerTone({ banner_tone: 'caution' })).toBe('caution')
    expect(bannerTone({ banner_tone: 'critical' })).toBe('critical')
  })
})
