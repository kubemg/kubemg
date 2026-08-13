import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { RotateCcw } from 'lucide-react'
import {
  clearDefaultRateCard,
  errorMessage,
  fetchDefaultRateCard,
  saveDefaultRateCard,
} from '../../api/client'
import type { RateCard, RatePreset } from '../../api/types'
import { Button, Notice, Panel } from '../../components/primitives'
import { RateCardFields, RatePresetChips } from '../../components/RateCardForm'
import { SettingsLayout } from '../../components/settings/SettingsLayout'
import {
  BLANK_RATE_DRAFT,
  draftIsPriced,
  draftOfRateCard,
  inputOfDraft,
} from '../../lib/ratecard'
import type { RateDraft } from '../../lib/ratecard'

/**
 * The rates every cost figure in the console is computed against.
 *
 * KubeMG calls no billing API and holds no cloud credential — a Cost Explorer
 * key would be the largest standing credential in a product that has spent
 * seven phases arguing a bastion needs none — so the rates are typed in here.
 * Everything downstream is an estimate and says so, on every screen the money
 * appears on.
 *
 * The presets are the part this page has to be careful with. A number KubeMG
 * puts on screen is one an operator will reasonably assume it stands behind, so
 * a preset is offered as something to **replace**: it fills the form and writes
 * its own provenance into the note, and the note travels with the card to
 * wherever the money is shown. They reflect no committed-use discount, no
 * reserved instance, no spot capacity and no enterprise agreement, and cloud
 * list prices move.
 *
 * This card is the installation default. A cluster on different hardware —
 * another cloud, or a rack somebody owns — overrides it from its own Cost page,
 * with the same form lifted into `components/RateCardForm.tsx`, because pricing
 * an on-prem cluster at EC2's list price is worse than pricing it at nothing.
 */

export function CostSettings() {
  const [presets, setPresets] = useState<RatePreset[]>([])
  const [stored, setStored] = useState<RateCard | null>(null)
  const [draft, setDraft] = useState<RateDraft>(BLANK_RATE_DRAFT)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const response = await fetchDefaultRateCard()
      setPresets(response.presets)
      setStored(response.rate_card)
      setDraft(draftOfRateCard(response.rate_card))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read the rate card.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function set<K extends keyof RateDraft>(field: K, value: RateDraft[K]) {
    setDraft((current) => ({ ...current, [field]: value }))
    setSaved(false)
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    setSaving(true)
    try {
      await saveDefaultRateCard(inputOfDraft(draft))
      setError(null)
      setSaved(true)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the rate card.'))
    } finally {
      setSaving(false)
    }
  }

  async function clear() {
    setSaving(true)
    try {
      await clearDefaultRateCard()
      setError(null)
      setSaved(false)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not clear the rate card.'))
    } finally {
      setSaving(false)
    }
  }

  const priced = draftIsPriced(draft)

  return (
    <SettingsLayout
      title="Cost"
      actions={
        stored ? (
          <Button variant="ghost" onClick={() => void clear()} disabled={saving}>
            <RotateCcw aria-hidden="true" className="size-4" />
            Clear
          </Button>
        ) : undefined
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {saved ? <Notice tone="ok">Rates saved.</Notice> : null}

      <form onSubmit={save} className="flex flex-col gap-4">
        <Panel
          title="Rates"
          description="What an hour of capacity costs. Every cost figure in the console is computed from these."
        >
          <p className="text-[12.5px] leading-relaxed text-muted">
            KubeMG calls no cloud billing API and holds no cloud credential, so these are entered
            rather than discovered. That is a decision: a billing integration would report what was
            invoiced, which arrives days late, is netted against commitments KubeMG cannot
            attribute to a Deployment, and covers a great deal that is not this cluster. What an
            operator can act on today is “these requests, at these rates, come to this”.
          </p>

          <div className="mt-4">
            <RatePresetChips
              presets={presets}
              active={draft.provider}
              idPrefix="default-rate"
              onApply={(next) => {
                setDraft(next)
                setSaved(false)
              }}
            />
          </div>

          <div className="mt-4 flex flex-col gap-4">
            <RateCardFields draft={draft} onChange={set} idPrefix="default-rate" />
          </div>

          {!priced ? (
            <Notice tone="info">
              With no rate set, the Cost page reports that the fleet is unpriced rather than
              showing zeroes. Idle volumes and load balancers are still found — what nothing is
              using is worth finding either way.
            </Notice>
          ) : null}

          <div className="mt-4 flex items-center gap-2">
            <Button type="submit" variant="primary" disabled={saving || loading}>
              Save rates
            </Button>
            {stored ? (
              <span className="text-[12px] text-faint">
                A cluster on different hardware can override this from its own Cost page.
              </span>
            ) : null}
          </div>
        </Panel>
      </form>
    </SettingsLayout>
  )
}
