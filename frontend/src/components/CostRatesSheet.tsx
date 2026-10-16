import { useState } from 'react'
import { deleteClusterCostRates, errorMessage, putClusterCostRates } from '../api/client'
import type { CostRates } from '../api/waste'
import { parseRate } from '../lib/waste'
import { Button, Field, Notice, Sheet, TextInput } from './primitives'

/**
 * A cluster's unit prices, set by an admin.
 *
 * A rate is the operator's, never a vendor's list price: it is what a core, a
 * GiB, a GiB of disk and a load balancer cost *this* cluster, and every figure
 * on the cost and waste page is requests times these. CPU and memory are asked
 * for together — a compute price is both. Storage and load balancers are
 * optional, and an empty field means "not priced", which the page then says,
 * rather than "free".
 */

type RateField = 'cpu_core_hour' | 'memory_gib_hour' | 'storage_gib_month' | 'load_balancer_hour'

const FIELDS: { key: RateField; label: string; hint: string; required: boolean }[] = [
  {
    key: 'cpu_core_hour',
    label: 'CPU, per core-hour',
    hint: 'What one core of a node costs for an hour.',
    required: true,
  },
  {
    key: 'memory_gib_hour',
    label: 'Memory, per GiB-hour',
    hint: 'What one GiB of a node’s memory costs for an hour.',
    required: true,
  },
  {
    key: 'storage_gib_month',
    label: 'Storage, per GiB-month',
    hint: 'Optional. What a GiB of provisioned volume costs for a month.',
    required: false,
  },
  {
    key: 'load_balancer_hour',
    label: 'Load balancer, per hour',
    hint: 'Optional. What one load balancer costs for an hour; 0 if it costs nothing.',
    required: false,
  },
]

function text(value: number | undefined): string {
  return value === undefined ? '' : String(value)
}

export function CostRatesSheet({
  clusterId,
  clusterName,
  rates,
  onClose,
  onSaved,
}: {
  clusterId: number
  clusterName: string
  rates: CostRates | null
  onClose: () => void
  /** Called after a save or a removal, so the page reads its figures again. */
  onSaved: () => void
}) {
  const [currency, setCurrency] = useState(rates?.currency ?? '')
  const [values, setValues] = useState<Record<RateField, string>>({
    cpu_core_hour: text(rates?.cpu_core_hour),
    memory_gib_hour: text(rates?.memory_gib_hour),
    storage_gib_month: text(rates?.storage_gib_month),
    load_balancer_hour: text(rates?.load_balancer_hour),
  })
  const [tried, setTried] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const currencyError =
    tried && !/^[A-Za-z]{3}$/.test(currency.trim())
      ? 'A three-letter currency code, such as USD or EUR.'
      : undefined
  const fieldError = (field: (typeof FIELDS)[number]): string | undefined => {
    if (!tried) return undefined
    const parsed = parseRate(values[field.key])
    if (parsed === null) return 'A number, such as 0.04.'
    if (parsed === undefined && field.required) return 'Needed: a compute price is both CPU and memory.'
    return undefined
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    setTried(true)
    const parsed = Object.fromEntries(
      FIELDS.map((field) => [field.key, parseRate(values[field.key])]),
    ) as Record<RateField, number | undefined | null>
    const invalid =
      !/^[A-Za-z]{3}$/.test(currency.trim()) ||
      FIELDS.some(
        (field) =>
          parsed[field.key] === null || (field.required && parsed[field.key] === undefined),
      )
    if (invalid) return

    setBusy(true)
    setError(null)
    try {
      await putClusterCostRates(clusterId, {
        currency: currency.trim().toUpperCase(),
        cpu_core_hour: parsed.cpu_core_hour as number,
        memory_gib_hour: parsed.memory_gib_hour as number,
        storage_gib_month: parsed.storage_gib_month ?? undefined,
        load_balancer_hour: parsed.load_balancer_hour ?? undefined,
      })
      onSaved()
      onClose()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the rates.'))
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      await deleteClusterCostRates(clusterId)
      onSaved()
      onClose()
    } catch (err) {
      setError(errorMessage(err, 'Could not remove the rates.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="Rates"
      eyebrow={clusterName}
      onClose={onClose}
      onSubmit={save}
      footer={
        <>
          {rates ? (
            <Button type="button" variant="danger" onClick={() => void remove()} disabled={busy}>
              Remove rates
            </Button>
          ) : null}
          <span className="flex-1" />
          <Button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save rates'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-5">
        {error ? <Notice tone="error">{error}</Notice> : null}
        <p className="text-[13px] leading-relaxed text-muted">
          Every figure on this cluster’s cost page is what its pods request, times these rates.
          They are yours, not a provider’s list price: take them from your bill or your contract.
          To split a node’s hourly price: where providers price the two separately, a core costs
          about as much as 8 to 9 GiB of memory — so, taking 8, a $0.192 node with 4 cores and 16
          GiB is $0.032 per core-hour and $0.004 per GiB-hour.
        </p>
        <Field
          label="Currency"
          htmlFor="rates-currency"
          hint="A three-letter code. Nothing is converted."
          error={currencyError}
        >
          <TextInput
            id="rates-currency"
            value={currency}
            maxLength={3}
            autoComplete="off"
            className="w-24 uppercase"
            onChange={(event) => setCurrency(event.target.value)}
          />
        </Field>
        {FIELDS.map((field) => (
          <Field
            key={field.key}
            label={field.label}
            htmlFor={`rates-${field.key}`}
            hint={field.hint}
            error={fieldError(field)}
          >
            <TextInput
              id={`rates-${field.key}`}
              inputMode="decimal"
              autoComplete="off"
              value={values[field.key]}
              className="w-40 font-data tabular-nums"
              onChange={(event) =>
                setValues((current) => ({ ...current, [field.key]: event.target.value }))
              }
            />
          </Field>
        ))}
      </div>
    </Sheet>
  )
}
