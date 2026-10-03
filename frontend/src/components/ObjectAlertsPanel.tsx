import { useState } from 'react'
import type { FormEvent } from 'react'
import { BellOff, Pencil, Trash2 } from 'lucide-react'
import {
  createSilence,
  deleteAlarm,
  errorMessage,
  fetchAlarms,
  fetchFiringAlerts,
  unconfigured,
} from '../api/client'
import type { Alarm, Cluster, FiringAlert, SilenceDuration } from '../api/types'
import {
  SILENCE_DURATIONS,
  alarmSentence,
  alertStateLabel,
  alertSummary,
  alertTone,
  severityTone,
} from '../lib/alerting'
import { queryKey, useCachedQuery } from '../lib/query'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'
import { Age, Button, IconButton, Notice, Panel, Pill, Select, TextInput } from './primitives'
import type { AlarmTarget } from './AlarmComposer'

/*
 * What the cluster's alerting says about one object, and the door to adding to
 * it. Two readings with two sources: the alerts are Alertmanager's (read as the
 * datasource, narrowed to the grant by the server); the alarms are the
 * PrometheusRules KubeMG wrote for this object (read as the caller). Either can
 * be missing for its own reason, and each says so rather than reading as "none".
 */

export function ObjectAlertsPanel({
  cluster,
  target,
  revision = 0,
  onEdit,
}: {
  cluster: Cluster
  target: AlarmTarget
  /** Bumped when an alarm was saved elsewhere, so the list is read again. */
  revision?: number
  onEdit: (alarm: Alarm) => void
}) {
  const confirm = useConfirm()
  const report = useResult()
  const filter = { namespace: target.namespace, kind: target.kind, name: target.name }

  const alerts = useCachedQuery(
    queryKey('object-alerts', cluster.id, target.namespace, target.kind, target.name),
    () => fetchFiringAlerts(cluster.id, filter),
    { live: true },
  )
  const alarms = useCachedQuery(
    queryKey('object-alarms', cluster.id, target.namespace, target.kind, target.name, revision),
    () => fetchAlarms(cluster.id, filter),
  )
  const [silencing, setSilencing] = useState<string | null>(null)

  const firing = alerts.data?.alerts ?? []
  const own = alarms.data?.items ?? []
  const noAlertmanager = alerts.error && unconfigured(alerts.error)

  async function remove(alarm: Alarm) {
    if (
      !(await confirm({
        eyebrow: 'Alarm',
        title: `Delete the "${alarm.condition_label}" alarm?`,
        body: `The PrometheusRule ${alarm.name} is removed from ${alarm.namespace}. Anything it is firing resolves once Prometheus re-reads its rules.`,
        confirmLabel: 'Delete',
      }))
    ) {
      return
    }
    try {
      await deleteAlarm(cluster.id, alarm.namespace, alarm.name)
      report({ tone: 'ok', title: 'Alarm deleted', body: `${alarm.condition_label} on ${target.name}.` })
      await alarms.refresh()
    } catch (err) {
      report({ tone: 'error', title: 'The alarm was not deleted', body: errorMessage(err, 'The cluster refused it.') })
    }
  }

  return (
    <Panel
      title="Alerts"
      eyebrow={firing.length > 0 ? `${firing.filter((a) => a.state === 'firing').length} firing` : 'Alerting'}
      description="What the cluster's Alertmanager is firing for this object, and the alarms kubemg wrote for it."
      bodyClassName="flex flex-col gap-4 px-5 py-4"
    >
      <section aria-label="Firing" className="flex flex-col gap-2">
        <h4 className="label">Firing now</h4>
        {noAlertmanager ? (
          <p className="text-[12.5px] text-muted">
            No Alertmanager is registered for this cluster, so what is firing cannot be read here. An
            administrator connects one under the cluster&rsquo;s datasources.
          </p>
        ) : alerts.error ? (
          <Notice tone="warn">{errorMessage(alerts.error, 'Could not read the Alertmanager.')}</Notice>
        ) : alerts.loading ? (
          <p className="text-[12.5px] text-muted">Reading the Alertmanager…</p>
        ) : firing.length === 0 ? (
          <p className="text-[12.5px] text-muted">Nothing is firing for this object.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-line-soft rounded-control border border-line-soft">
            {firing.map((alert) => (
              <AlertRow
                key={alert.fingerprint}
                alert={alert}
                canSilence={alert.can_silence}
                silencing={silencing === alert.fingerprint}
                onSilence={() => setSilencing(silencing === alert.fingerprint ? null : alert.fingerprint)}
                onSilenced={async () => {
                  setSilencing(null)
                  report({ tone: 'ok', title: 'Alert silenced', body: alert.name })
                  await alerts.refresh()
                }}
                clusterId={cluster.id}
              />
            ))}
          </ul>
        )}
      </section>

      <section aria-label="Alarms" className="flex flex-col gap-2">
        <h4 className="label">Alarms kubemg wrote</h4>
        {alarms.error ? (
          <Notice tone="warn">{errorMessage(alarms.error, 'Could not read the alarms.')}</Notice>
        ) : alarms.data && !alarms.data.available ? (
          <p className="text-[12.5px] text-muted">{alarms.data.reason}</p>
        ) : alarms.loading ? (
          <p className="text-[12.5px] text-muted">Reading the alarms…</p>
        ) : own.length === 0 ? (
          <p className="text-[12.5px] text-muted">
            None yet. <span className="text-fg">Create alarm</span> at the top of this drawer writes one for this{' '}
            {target.label.toLowerCase()}.
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-line-soft rounded-control border border-line-soft">
            {own.map((alarm) => (
              <li key={alarm.name} className="flex flex-wrap items-center gap-3 px-3 py-2.5">
                <Pill tone={severityTone(alarm.severity)}>{alarm.severity}</Pill>
                <div className="min-w-0 flex-1">
                  <p className="text-[13.5px] text-fg">{alarmSentence(alarm)}</p>
                  <p className="mt-0.5 truncate text-[12px] text-muted">
                    {alarm.created_by ? `by ${alarm.created_by} · ` : ''}
                    <Age iso={alarm.created_at} />
                    {alarm.note ? ` · ${alarm.note}` : ''}
                  </p>
                </div>
                <IconButton label="Change alarm" onClick={() => onEdit(alarm)}>
                  <Pencil aria-hidden="true" className="size-4" />
                </IconButton>
                <IconButton label="Delete alarm" tone="danger" onClick={() => void remove(alarm)}>
                  <Trash2 aria-hidden="true" className="size-4" />
                </IconButton>
              </li>
            ))}
          </ul>
        )}
      </section>
    </Panel>
  )
}

function AlertRow({
  alert,
  canSilence,
  silencing,
  onSilence,
  onSilenced,
  clusterId,
}: {
  alert: FiringAlert
  canSilence: boolean
  silencing: boolean
  onSilence: () => void
  onSilenced: () => void | Promise<void>
  clusterId: number
}) {
  return (
    <li className="flex flex-col gap-2 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-3">
        <Pill tone={alertTone(alert)}>{alertStateLabel(alert)}</Pill>
        <div className="min-w-0 flex-1">
          <p className="truncate text-[13.5px] text-fg" title={alertSummary(alert)}>
            {alertSummary(alert)}
          </p>
          <p className="mt-0.5 text-[12px] text-muted">
            <span className="font-data">{alert.name}</span> · since <Age iso={alert.starts_at} />
            {alert.kubemg ? ' · kubemg alarm' : ''}
          </p>
        </div>
        {canSilence && alert.state === 'firing' ? (
          <Button type="button" size="sm" variant={silencing ? 'primary' : 'secondary'} onClick={onSilence}>
            <BellOff aria-hidden="true" className="size-4" />
            Silence
          </Button>
        ) : null}
      </div>
      {silencing ? <SilenceForm clusterId={clusterId} alert={alert} onDone={onSilenced} onCancel={onSilence} /> : null}
    </li>
  )
}

/** SilenceForm mutes exactly this alert — the server copies its labels. */
export function SilenceForm({
  clusterId,
  alert,
  onDone,
  onCancel,
}: {
  clusterId: number
  alert: FiringAlert
  onDone: () => void | Promise<void>
  onCancel: () => void
}) {
  const [duration, setDuration] = useState<SilenceDuration>('4h')
  const [comment, setComment] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await createSilence(clusterId, alert.fingerprint, duration, comment.trim())
      await onDone()
    } catch (err) {
      setError(errorMessage(err, 'Could not silence the alert.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-2 rounded-control bg-raised/60 p-3" aria-label="Silence alert">
      {error ? <Notice tone="error">{error}</Notice> : null}
      <div className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col gap-1">
          <span className="label">For</span>
          <Select
            size="sm"
            aria-label="Silence duration"
            value={duration}
            onChange={(event) => setDuration(event.target.value as SilenceDuration)}
          >
            {SILENCE_DURATIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </Select>
        </label>
        <label className="flex min-w-[12rem] flex-1 flex-col gap-1">
          <span className="label">Why</span>
          <TextInput
            required
            maxLength={500}
            aria-label="Reason"
            placeholder="Deploy in progress, tracked in INC-123"
            value={comment}
            onChange={(event) => setComment(event.target.value)}
          />
        </label>
        <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" size="sm" variant="primary" disabled={busy || !comment.trim()}>
          {busy ? 'Silencing…' : 'Silence'}
        </Button>
      </div>
      <p className="text-[11.5px] text-faint">
        Matches every label of this alert exactly, and is recorded in the audit trail under your name.
      </p>
    </form>
  )
}
