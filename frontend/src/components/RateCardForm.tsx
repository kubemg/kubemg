import type { RateCard, RatePreset } from '../api/types'
import { draftOfPreset } from '../lib/ratecard'
import type { RateDraft } from '../lib/ratecard'
import { Chip, Field, Select, TextArea, TextInput } from './primitives'

/**
 * The rate card form, in one place because it is entered in two.
 *
 * The installation default is edited in Settings and a cluster's override on
 * that cluster's own Cost page — the same six rates, the same presets, and the
 * same sentences about what a preset is and is not. Two copies of a form whose
 * whole job is provenance would be two copies that drift, and the one that
 * drifts is the one nobody looks at. The draft shape these render and the
 * conversions either side of it live in `lib/ratecard.ts`.
 */

/**
 * RatePresetChips offers a starting point as something to **replace**. Applying
 * one fills the whole form, its own note included, so the provenance of a number
 * survives the operator who applied it.
 */
export function RatePresetChips({
  presets,
  active,
  onApply,
  idPrefix,
}: {
  presets: RatePreset[]
  active: RateCard['provider']
  onApply: (draft: RateDraft) => void
  idPrefix: string
}) {
  if (presets.length === 0) return null
  return (
    <div>
      <p className="label mb-2">Start from</p>
      <div className="flex flex-wrap gap-2">
        {presets.map((preset) => (
          <Chip
            key={`${idPrefix}-${preset.provider}`}
            active={active === preset.provider}
            onClick={() => onApply(draftOfPreset(preset))}
          >
            {preset.label}
          </Chip>
        ))}
      </div>
      <p className="mt-2 text-[11.5px] leading-relaxed text-faint">
        A preset is something to replace, not something to accept. Each is one general-purpose
        instance family's on-demand list price in one region, split into a CPU share and a memory
        share — it reflects no discount you have negotiated, and list prices move.
      </p>
    </div>
  )
}

/**
 * RateCardFields is the six rates and the note that travels with them. The
 * `idPrefix` keeps the two instances' labels pointing at their own inputs when
 * both forms exist in one document.
 */
export function RateCardFields({
  draft,
  onChange,
  idPrefix,
}: {
  draft: RateDraft
  onChange: <K extends keyof RateDraft>(field: K, value: RateDraft[K]) => void
  idPrefix: string
}) {
  const id = (name: string) => `${idPrefix}-${name}`

  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Currency" htmlFor={id('currency')} hint="ISO 4217. Echoed, never converted.">
          <TextInput
            id={id('currency')}
            value={draft.currency}
            maxLength={3}
            onChange={(event) => onChange('currency', event.target.value.toUpperCase())}
          />
        </Field>

        <Field
          label="Price list"
          htmlFor={id('provider')}
          hint="Recorded so you can see later where these came from."
        >
          <Select
            id={id('provider')}
            value={draft.provider}
            onChange={(event) => onChange('provider', event.target.value as RateCard['provider'])}
          >
            <option value="aws">AWS</option>
            <option value="gcp">Google Cloud</option>
            <option value="azure">Azure</option>
            <option value="custom">Your own rates</option>
          </Select>
        </Field>

        <Field
          label="CPU, per vCPU-hour"
          htmlFor={id('cpu')}
          hint="Priced per hour, the way a cloud's own list quotes it."
        >
          <TextInput
            id={id('cpu')}
            inputMode="decimal"
            placeholder="0.0353"
            value={draft.cpu_core_hour}
            onChange={(event) => onChange('cpu_core_hour', event.target.value)}
          />
        </Field>

        <Field label="Memory, per GiB-hour" htmlFor={id('memory')} hint="Also hourly.">
          <TextInput
            id={id('memory')}
            inputMode="decimal"
            placeholder="0.0038"
            value={draft.memory_gib_hour}
            onChange={(event) => onChange('memory_gib_hour', event.target.value)}
          />
        </Field>

        <Field
          label="Storage, per GiB-month"
          htmlFor={id('storage')}
          hint="What a provisioned volume costs — a half-empty disk is billed for its whole size."
        >
          <TextInput
            id={id('storage')}
            inputMode="decimal"
            placeholder="0.08"
            value={draft.storage_gib_month}
            onChange={(event) => onChange('storage_gib_month', event.target.value)}
          />
        </Field>

        <Field
          label="Load balancer, per month"
          htmlFor={id('lb')}
          hint="The standing charge before any traffic. Bandwidth is not modelled — KubeMG cannot see a byte of it."
        >
          <TextInput
            id={id('lb')}
            inputMode="decimal"
            placeholder="16.43"
            value={draft.load_balancer_month}
            onChange={(event) => onChange('load_balancer_month', event.target.value)}
          />
        </Field>
      </div>

      <Field
        label="What these rates are"
        htmlFor={id('note')}
        hint="Shown wherever the money is. An estimate whose provenance is off screen is one nobody can argue with."
      >
        <TextArea
          id={id('note')}
          rows={3}
          value={draft.note}
          onChange={(event) => onChange('note', event.target.value)}
        />
      </Field>
    </>
  )
}
