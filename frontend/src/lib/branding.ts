import type { BannerTone, Branding, Cluster } from '../api/types'

/**
 * What the rail's chip says for a cluster.
 *
 * The stored short name wins, and it is stored precisely because the derivation
 * below cannot be relied on past a handful of clusters: `prod-eu-west-1` and
 * `prod-eu-west-2` both reduce to `PEW`, and a rail whose chips are ambiguous is
 * one nobody can navigate by muscle memory — which is the only thing a rail is
 * for.
 *
 * The derivation stays as the fallback rather than being replaced by it. A fleet
 * registered before short names existed has none, and it should look exactly as
 * it did rather than showing a column of blanks until somebody edits eleven
 * clusters.
 */
export function railChip(cluster: Pick<Cluster, 'name' | 'short_name'>): string {
  const chosen = (cluster.short_name ?? '').trim()
  if (chosen !== '') return chosen.toUpperCase()
  return deriveChip(cluster.name)
}

/**
 * The chip a name produces on its own: initials across a separated name, or the
 * first three characters of a single word.
 *
 * This is also what the registration form offers as a starting point, so that
 * choosing a short name is editing a suggestion rather than inventing one from
 * nothing — and so that an operator who accepts the suggestion gets exactly what
 * the console would have drawn anyway.
 */
export function deriveChip(name: string): string {
  const parts = name.split(/[^a-zA-Z0-9]+/).filter(Boolean)
  if (parts.length === 0) return name.slice(0, 3).toUpperCase()
  if (parts.length === 1) return parts[0].slice(0, 3).toUpperCase()
  return parts
    .slice(0, 3)
    .map((part) => part[0])
    .join('')
    .toUpperCase()
}

/** How many characters the chip can hold at the size it is drawn. Mirrors
    db.MaxShortNameLen, which is what actually enforces it. */
export const MAX_SHORT_NAME = 4

/**
 * The client's copy of the server's normalizer, so the field shows what will be
 * stored rather than what was typed.
 *
 * It normalizes rather than refuses for the same reason the server does: every
 * input it changes is one somebody meant something reasonable by, and refusing
 * `eu-west-1` over a hyphen would be pedantry about a label. The server folds it
 * again on the way in, so this is a courtesy and never the enforcement.
 */
export function normalizeShortName(raw: string): string {
  return raw
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, '')
    .slice(0, MAX_SHORT_NAME)
}

/**
 * Whether there is a banner to draw at all.
 *
 * A tone without text is not a banner — the server already refuses to report
 * one, and this is the console agreeing rather than trusting.
 */
export function hasBanner(branding: Branding | null): branding is Branding & { banner_text: string } {
  return Boolean(branding?.banner_text?.trim())
}

/**
 * The classes a banner tone is drawn in.
 *
 * They are the deck's semantic tokens, not a palette of this feature's own: an
 * operator who has learned what amber means on a pod's phase must not have to
 * learn a second meaning for it here. Lime is deliberately absent — it is the
 * interactive accent, and a banner is not pressable.
 */
export const BANNER_TONE_CLASS: Record<BannerTone, string> = {
  neutral: 'bg-raised text-fg border-line',
  caution: 'bg-warn-soft text-warn border-warn/40',
  critical: 'bg-danger-soft text-danger border-danger/40',
}

/** The tone to draw with, defaulting the way the server's reader does. */
export function bannerTone(branding: Branding | null): BannerTone {
  const tone = branding?.banner_tone
  if (tone === 'caution' || tone === 'critical') return tone
  return 'neutral'
}
