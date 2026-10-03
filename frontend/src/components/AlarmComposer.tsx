import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { BellPlus, X } from 'lucide-react'
import axios from 'axios'
import { createAlarm, errorMessage, fetchAlarmCatalogue, unconfigured, updateAlarm } from '../api/client'
import type { Alarm, AlarmSeverity, Cluster, ResourceCondition, Pod } from '../api/types'
import { forWords, suggestCondition } from '../lib/alerting'
import { queryKey, useCachedQuery } from '../lib/query'
import { Button, Field, IconButton, Notice, Segmented, Select, TextArea, TextInput } from './primitives'

/*
 * "Create an alarm for this": a condition from the server's catalogue, its one
 * number, how long it must hold, and how loud it is. Nothing here is PromQL —
 * the server writes the rule — and the object and namespace are the drawer's,
 * never typed.
 *
 * It is drawn inline over the drawer's tabs, the way Scale and Restart are, so
 * the conditions and events that prompted it stay on screen while it is filled.
 */

export interface AlarmTarget {
  kind: string
  /** The singular label: "Deployment". */
  label: string
  name: string
  namespace: string
}

const SEVERITY_OPTIONS: { value: AlarmSeverity; label: string }[] = [
  { value: 'info', label: 'Info' },
  { value: 'warning', label: 'Warning' },
  { value: 'critical', label: 'Critical' },
]

export function AlarmComposer({
  cluster,
  target,
  editing,
  conditions,
  pod,
  onClose,
  onSaved,
}: {
  cluster: Cluster
  target: AlarmTarget
  /** An existing alarm to change; its object and condition stay fixed. */
  editing?: Alarm | null
  /** The object's own conditions, which pick the condition the form opens on. */
  conditions?: ResourceCondition[]
  pod?: Pod
  onClose: () => void
  onSaved: (alarm: Alarm) => void | Promise<void>
}) {
  const catalogue = useCachedQuery(queryKey('alarm-catalogue'), fetchAlarmCatalogue)
  const available = catalogue.data?.conditions[target.kind] ?? []

  const [picked, setPicked] = useState<string | null>(editing?.condition ?? null)
  const conditionKey = picked ?? suggestCondition(target.kind, available, conditions, pod) ?? ''
  const condition = available.find((entry) => entry.key === conditionKey)

  // Per-condition values are kept until submitted, so switching conditions and
  // back does not lose what was typed.
  const [threshold, setThreshold] = useState<string>(
    editing?.threshold !== undefined ? String(editing.threshold) : '',
  )
  const [duration, setDuration] = useState<string>(editing?.for ?? '')
  const [severity, setSeverity] = useState<AlarmSeverity | ''>(editing?.severity ?? '')
  const [note, setNote] = useState(editing?.note ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Opened from the Overview's panel, the composer lands above the tabs — out
  // of view on a long drawer — so it brings itself into view once.
  const formRef = useRef<HTMLFormElement>(null)
  useEffect(() => {
    formRef.current?.scrollIntoView?.({ block: 'nearest' })
  }, [])

  const effectiveFor = duration || condition?.default_for || '5m'
  const effectiveSeverity = severity || condition?.default_severity || 'warning'
  const effectiveThreshold = threshold === '' ? condition?.threshold?.default : Number(threshold)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!condition) return
    setBusy(true)
    setError(null)
    const shared = {
      namespace: target.namespace,
      threshold: condition.threshold ? effectiveThreshold : undefined,
      for: effectiveFor,
      severity: effectiveSeverity,
      note: note.trim() || undefined,
    }
    try {
      const saved = editing
        ? await updateAlarm(cluster.id, { ...shared, name: editing.name })
        : await createAlarm(cluster.id, {
            ...shared,
            kind: target.kind,
            name: target.name,
            condition: condition.key,
          })
      await onSaved(saved)
    } catch (err) {
      if (unconfigured(err)) {
        setError(errorMessage(err, 'This cluster has no Alertmanager registered.'))
      } else if (axios.isAxiosError(err) && err.response?.status === 403) {
        setError(
          `${errorMessage(err, 'The cluster refused it.')} — creating an alarm takes write access to PrometheusRules in ${target.namespace}.`,
        )
      } else {
        setError(errorMessage(err, 'Could not save the alarm.'))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      ref={formRef}
      onSubmit={submit}
      className="card flex shrink-0 flex-col gap-4 border-accent/40 px-5 py-4"
      aria-label={editing ? 'Change alarm' : 'Create alarm'}
    >
      <div className="flex items-start gap-3">
        <span className="grid size-8 shrink-0 place-items-center rounded-full bg-accent-soft text-accent">
          <BellPlus aria-hidden="true" className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-[15px] font-semibold text-fg">
            {editing ? 'Change alarm' : 'Create an alarm'} for{' '}
            <span className="font-data">{target.name}</span>
          </h3>
          <p className="mt-0.5 text-[12.5px] text-muted">
            Written to the cluster as a PrometheusRule in {target.namespace}. Its Prometheus evaluates it
            and its Alertmanager routes it, so it keeps working while kubemg is down.
          </p>
        </div>
        <IconButton label="Close" onClick={onClose}>
          <X aria-hidden="true" className="size-4" />
        </IconButton>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {catalogue.error ? (
        <Notice tone="error">{errorMessage(catalogue.error, 'Could not read the alarm conditions.')}</Notice>
      ) : null}

      {catalogue.data && available.length === 0 ? (
        <Notice tone="info">kubemg has no alarm conditions for a {target.label} yet.</Notice>
      ) : null}

      {available.length > 0 ? (
        <fieldset className="flex flex-col gap-2">
          <legend className="label mb-2">Condition</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {available.map((entry) => {
              const selected = entry.key === conditionKey
              return (
                <label
                  key={entry.key}
                  className={`flex cursor-pointer items-start gap-2.5 rounded-control border px-3 py-2.5 transition-colors ${
                    selected ? 'border-accent bg-accent-soft/40' : 'border-line hover:bg-raised'
                  } ${editing && !selected ? 'pointer-events-none opacity-40' : ''}`}
                >
                  <input
                    type="radio"
                    name="alarm-condition"
                    className="mt-0.5 size-3.5 accent-[var(--color-accent)]"
                    checked={selected}
                    disabled={Boolean(editing)}
                    onChange={() => setPicked(entry.key)}
                  />
                  <span className="min-w-0">
                    <span className="block text-[13.5px] font-medium text-fg">{entry.label}</span>
                    <span className="mt-0.5 block text-[12px] leading-snug text-muted">{entry.description}</span>
                  </span>
                </label>
              )
            })}
          </div>
        </fieldset>
      ) : null}

      {condition ? (
        <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          {condition.threshold ? (
            <Field
              label={condition.threshold.label}
              htmlFor="alarm-threshold"
              hint={`Between ${condition.threshold.min} and ${condition.threshold.max}${condition.threshold.unit ?? ''}.`}
            >
              <TextInput
                id="alarm-threshold"
                type="number"
                inputMode="decimal"
                min={condition.threshold.min}
                max={condition.threshold.max}
                className="font-data tabular-nums"
                placeholder={String(condition.threshold.default)}
                value={threshold}
                onChange={(event) => setThreshold(event.target.value)}
              />
            </Field>
          ) : null}
          <Field label="Fires when it holds" htmlFor="alarm-for" hint={
              effectiveFor === '0m'
                ? 'Fires the moment the condition is true.'
                : `Fires once the condition has held ${forWords(effectiveFor)}.`
            }>
            <Select id="alarm-for" value={effectiveFor} onChange={(event) => setDuration(event.target.value)}>
              {(catalogue.data?.durations ?? []).map((value) => (
                <option key={value} value={value}>
                  {forWords(value)}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      ) : null}

      {condition ? (
        <Field label="Severity" htmlFor="alarm-severity" hint="What Alertmanager routing matches on.">
          <Segmented<AlarmSeverity>
            id="alarm-severity"
            ariaLabel="Severity"
            value={effectiveSeverity}
            onChange={setSeverity}
            options={SEVERITY_OPTIONS}
          />
        </Field>
      ) : null}

      {condition ? (
        <Field label="Note" htmlFor="alarm-note" hint="Optional. Added to the alert's description — who to call, what to check.">
          <TextArea id="alarm-note" rows={2} maxLength={500} value={note} onChange={(event) => setNote(event.target.value)} />
        </Field>
      ) : null}

      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={busy || !condition}>
          <BellPlus aria-hidden="true" className="size-4" />
          {busy ? 'Saving…' : editing ? 'Save alarm' : 'Create alarm'}
        </Button>
      </div>
    </form>
  )
}
