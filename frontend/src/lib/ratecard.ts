import type { RateCardInput } from '../api/client'
import type { RateCard, RatePreset } from '../api/types'

/**
 * A rate card as a form holds it, and the conversions either side of that.
 *
 * The same card is entered in two places — the installation default in Settings
 * and a cluster's override on its own Cost page — so the draft shape and its
 * conversions live here rather than in either screen. The fields are strings
 * because an empty box has to stay distinguishable from a rate somebody
 * deliberately set to zero; numbers appear once, on the way out.
 */

export interface RateDraft {
  provider: RateCard['provider']
  currency: string
  cpu_core_hour: string
  memory_gib_hour: string
  storage_gib_month: string
  load_balancer_month: string
  note: string
}

export const BLANK_RATE_DRAFT: RateDraft = {
  provider: 'custom',
  currency: 'USD',
  cpu_core_hour: '',
  memory_gib_hour: '',
  storage_gib_month: '',
  load_balancer_month: '',
  note: '',
}

export function draftOfRateCard(card: RateCard | null): RateDraft {
  if (!card) return BLANK_RATE_DRAFT
  return {
    provider: card.provider,
    currency: card.currency,
    cpu_core_hour: rateString(card.cpu_core_hour),
    memory_gib_hour: rateString(card.memory_gib_hour),
    storage_gib_month: rateString(card.storage_gib_month),
    load_balancer_month: rateString(card.load_balancer_month),
    note: card.note ?? '',
  }
}

/**
 * draftOfPreset fills the whole form from a starting point, its note included:
 * a preset's provenance is stored with the card so it survives the operator who
 * applied it, which is the only reason a preset carries prose at all.
 */
export function draftOfPreset(preset: RatePreset): RateDraft {
  return {
    provider: preset.provider,
    currency: preset.currency,
    cpu_core_hour: rateString(preset.cpu_core_hour),
    memory_gib_hour: rateString(preset.memory_gib_hour),
    storage_gib_month: rateString(preset.storage_gib_month),
    load_balancer_month: rateString(preset.load_balancer_month),
    note: preset.note,
  }
}

/** A zero rate reads as unset, which is what an empty box means on the way back in. */
function rateString(value: number): string {
  return value > 0 ? String(value) : ''
}

export function rateNumber(value: string): number {
  const parsed = Number(value.trim())
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}

export function inputOfDraft(draft: RateDraft): RateCardInput {
  return {
    provider: draft.provider,
    currency: draft.currency.trim().toUpperCase() || 'USD',
    cpu_core_hour: rateNumber(draft.cpu_core_hour),
    memory_gib_hour: rateNumber(draft.memory_gib_hour),
    storage_gib_month: rateNumber(draft.storage_gib_month),
    load_balancer_month: rateNumber(draft.load_balancer_month),
    note: draft.note.trim(),
  }
}

/** priced is what separates "these are the rates" from "there are no rates". */
export function draftIsPriced(draft: RateDraft): boolean {
  return (
    rateNumber(draft.cpu_core_hour) > 0 ||
    rateNumber(draft.memory_gib_hour) > 0 ||
    rateNumber(draft.storage_gib_month) > 0 ||
    rateNumber(draft.load_balancer_month) > 0
  )
}
