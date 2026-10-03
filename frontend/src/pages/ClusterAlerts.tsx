import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { BellOff, BellRing, Pencil, Siren, Trash2 } from 'lucide-react'
import {
  deleteAlarm,
  errorMessage,
  expireSilence,
  fetchAlarms,
  fetchFiringAlerts,
  fetchSilences,
  unconfigured,
} from '../api/client'
import type { Alarm, AlertSilence, Cluster } from '../api/types'
import { AlarmComposer } from '../components/AlarmComposer'
import { AppShell } from '../components/AppShell'
import { LiveRefresh } from '../components/LiveRefresh'
import { SilenceForm } from '../components/ObjectAlertsPanel'
import { Age, Button, EmptyState, IconButton, Notice, Pill, StatTile } from '../components/primitives'
import { TableSkeleton } from '../components/SkeletonLoader'
import {
  alarmSentence,
  alertObject,
  alertStateLabel,
  alertSummary,
  alertTone,
  kindTitle,
  severityTone,
} from '../lib/alerting'
import { resourceHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useConfirm } from '../state/confirm-context'
import { useResult } from '../state/result-context'

/*
 * A cluster's alerting in one place: what its Alertmanager is firing, the
 * alarms kubemg wrote into it, and what somebody has silenced. Everything is
 * narrowed to the caller's grant by the server — a scoped developer sees their
 * namespaces' alerts and silences, not the cluster's.
 *
 * Creating an alarm does not start here. It starts on the object, from its
 * drawer, because "alarm on this" needs a *this*; this page is for reviewing
 * and tuning what exists.
 */

function ObjectLink({
  clusterId,
  namespace,
  kind,
  title,
  name,
}: {
  clusterId: number
  namespace?: string
  kind: string
  title: string
  name: string
}) {
  return (
    <Link
      to={resourceHref(clusterId, kind, namespace ? `?ns=${encodeURIComponent(namespace)}` : '')}
      className="font-data text-fg hover:text-accent hover:underline"
      title={`${title} ${name}`}
    >
      <span className="text-faint">{title} </span>
      {name}
    </Link>
  )
}

export function ClusterAlerts() {
  const { clusters, loading: clustersLoading } = useClusters()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)
  const cluster = clusters.find((entry) => entry.id === clusterId) ?? null

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Alerts">
        <div className="card">
          <EmptyState icon={<BellRing aria-hidden="true" className="size-5" />} title="That cluster is not registered">
            Pick a cluster from the fleet list to read its alerts.
          </EmptyState>
        </div>
      </AppShell>
    )
  }
  if (!cluster) return <AppShell title="Alerts">{null}</AppShell>
  return <AlertsBody cluster={cluster} />
}

function AlertsBody({ cluster }: { cluster: Cluster }) {
  const { user } = useAuth()
  const confirm = useConfirm()
  const report = useResult()
  const tunnel = cluster.connection_mode === 'agent' && cluster.agent_attached

  const alerts = useCachedQuery(queryKey('cluster-alerts', cluster.id), () => fetchFiringAlerts(cluster.id), {
    live: true,
  })
  const silences = useCachedQuery(queryKey('cluster-silences', cluster.id), () => fetchSilences(cluster.id), {
    live: true,
  })
  const [revision, setRevision] = useState(0)
  const alarms = useCachedQuery(
    tunnel ? queryKey('cluster-alarms', cluster.id, revision) : null,
    () => fetchAlarms(cluster.id, { allNamespaces: true }),
  )
  const [editing, setEditing] = useState<Alarm | null>(null)
  const [silencing, setSilencing] = useState<string | null>(null)

  const noAlertmanager = Boolean(alerts.error && unconfigured(alerts.error))
  const firing = alerts.data?.alerts ?? []
  const firingNow = firing.filter((alert) => alert.state === 'firing')
  const own = alarms.data?.items ?? []
  const live = silences.data ?? []

  async function removeAlarm(alarm: Alarm) {
    if (
      !(await confirm({
        eyebrow: 'Alarm',
        title: `Delete the "${alarm.condition_label}" alarm on ${alarm.target}?`,
        body: `The PrometheusRule ${alarm.name} is removed from ${alarm.namespace}.`,
        confirmLabel: 'Delete',
      }))
    ) {
      return
    }
    try {
      await deleteAlarm(cluster.id, alarm.namespace, alarm.name)
      report({ tone: 'ok', title: 'Alarm deleted', body: `${alarm.condition_label} on ${alarm.target}.` })
      setRevision((value) => value + 1)
    } catch (err) {
      report({ tone: 'error', title: 'The alarm was not deleted', body: errorMessage(err, 'The cluster refused it.') })
    }
  }

  async function endSilence(silence: AlertSilence) {
    if (
      !(await confirm({
        eyebrow: 'Silence',
        title: 'End this silence now?',
        body: 'Anything it is muting starts notifying again straight away.',
        confirmLabel: 'End silence',
      }))
    ) {
      return
    }
    try {
      await expireSilence(cluster.id, silence.id)
      report({ tone: 'ok', title: 'Silence ended', body: silence.comment })
      await Promise.all([silences.refresh(), alerts.refresh()])
    } catch (err) {
      report({ tone: 'error', title: 'The silence was not ended', body: errorMessage(err, 'Alertmanager refused it.') })
    }
  }

  return (
    <AppShell title="Alerts" fullWidth actions={<LiveRefresh query={alerts} />}>
      <div className="flex min-w-0 flex-col gap-4">
        {noAlertmanager ? (
          <Notice tone="info">
            No Alertmanager is registered for {cluster.name}, so nothing firing can be read.{' '}
            {user?.role === 'admin' ? (
              <>
                Connect one under{' '}
                <Link to={`/clusters/${cluster.id}/summary`} className="text-accent hover:underline">
                  the cluster&rsquo;s datasources
                </Link>
                ; alarms need it too.
              </>
            ) : (
              'An administrator connects one under the cluster’s datasources; alarms need it too.'
            )}
          </Notice>
        ) : alerts.error ? (
          <Notice tone="error">{errorMessage(alerts.error, 'Could not read the Alertmanager.')}</Notice>
        ) : null}

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <StatTile
            icon={Siren}
            label="Firing"
            value={alerts.data ? firingNow.length : '—'}
            tone={firingNow.some((alert) => alert.severity === 'critical') ? 'danger' : firingNow.length ? 'warn' : 'neutral'}
          />
          <StatTile icon={BellOff} label="Silenced" value={silences.data ? live.length : '—'} />
          <StatTile icon={BellRing} label="Alarms kubemg wrote" value={alarms.data?.available ? own.length : '—'} />
        </div>

        {editing ? (
          <AlarmComposer
            cluster={cluster}
            target={{ kind: editing.kind, label: editing.kind, name: editing.target, namespace: editing.namespace }}
            editing={editing}
            onClose={() => setEditing(null)}
            onSaved={() => {
              setEditing(null)
              setRevision((value) => value + 1)
              report({ tone: 'ok', title: 'Alarm saved', body: `${editing.condition_label} on ${editing.target}.` })
            }}
          />
        ) : null}

        <section className="card min-w-0 overflow-hidden" aria-label="Firing">
          <header className="border-b border-line-soft px-5 pt-4 pb-3.5">
            <h2 className="text-[16px] font-bold text-fg">Firing</h2>
            <p className="mt-0.5 text-[13px] text-muted">
              Everything the Alertmanager holds that your grant covers, worst first.
            </p>
          </header>
          {alerts.loading && !alerts.data ? <TableSkeleton columns={4} rows={4} label="Reading alerts" /> : null}
          {alerts.data && firing.length === 0 ? (
            <p className="px-5 py-8 text-center text-[13px] text-muted">Nothing is firing.</p>
          ) : null}
          {firing.length > 0 ? (
            <ul className="divide-y divide-line-soft">
              {firing.map((alert) => {
                const object = alertObject(alert)
                return (
                  <li key={alert.fingerprint} className="flex flex-col gap-2 px-5 py-3">
                    <div className="flex flex-wrap items-center gap-3">
                      <Pill tone={alertTone(alert)}>{alertStateLabel(alert)}</Pill>
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-[13.5px] text-fg" title={alertSummary(alert)}>
                          {alertSummary(alert)}
                        </p>
                        <p className="mt-0.5 flex flex-wrap gap-x-2 text-[12px] text-muted">
                          <span className="font-data">{alert.name}</span>
                          {alert.namespace ? <span className="font-data">{alert.namespace}</span> : <span>cluster</span>}
                          {object ? (
                            <ObjectLink clusterId={cluster.id} namespace={alert.namespace} {...object} />
                          ) : null}
                          <span>
                            since <Age iso={alert.starts_at} />
                          </span>
                        </p>
                      </div>
                      {alert.can_silence && alert.state === 'firing' ? (
                        <Button
                          type="button"
                          size="sm"
                          variant={silencing === alert.fingerprint ? 'primary' : 'secondary'}
                          onClick={() => setSilencing(silencing === alert.fingerprint ? null : alert.fingerprint)}
                        >
                          <BellOff aria-hidden="true" className="size-4" />
                          Silence
                        </Button>
                      ) : null}
                    </div>
                    {silencing === alert.fingerprint ? (
                      <SilenceForm
                        clusterId={cluster.id}
                        alert={alert}
                        onCancel={() => setSilencing(null)}
                        onDone={async () => {
                          setSilencing(null)
                          report({ tone: 'ok', title: 'Alert silenced', body: alert.name })
                          await Promise.all([alerts.refresh(), silences.refresh()])
                        }}
                      />
                    ) : null}
                  </li>
                )
              })}
            </ul>
          ) : null}
        </section>

        <section className="card min-w-0 overflow-hidden" aria-label="Alarms">
          <header className="border-b border-line-soft px-5 pt-4 pb-3.5">
            <h2 className="text-[16px] font-bold text-fg">Alarms kubemg wrote</h2>
            <p className="mt-0.5 text-[13px] text-muted">
              PrometheusRules created from an object&rsquo;s drawer. Open the object to add one.
            </p>
          </header>
          {!tunnel ? (
            <p className="px-5 py-6 text-[13px] text-muted">
              Alarms are read from the cluster through its agent, which is not connected.
            </p>
          ) : alarms.error ? (
            <div className="px-5 py-4">
              <Notice tone="warn">{errorMessage(alarms.error, 'Could not read the alarms.')}</Notice>
            </div>
          ) : alarms.data && !alarms.data.available ? (
            <p className="px-5 py-6 text-[13px] text-muted">{alarms.data.reason}</p>
          ) : alarms.loading ? (
            <TableSkeleton columns={4} rows={3} label="Reading alarms" />
          ) : own.length === 0 ? (
            <p className="px-5 py-8 text-center text-[13px] text-muted">
              No alarms yet. Open a Deployment, pod or volume claim and choose{' '}
              <span className="text-fg">Create alarm</span>.
            </p>
          ) : (
            <ul className="divide-y divide-line-soft">
              {own.map((alarm) => (
                <li key={`${alarm.namespace}/${alarm.name}`} className="flex flex-wrap items-center gap-3 px-5 py-3">
                  <Pill tone={severityTone(alarm.severity)}>{alarm.severity}</Pill>
                  <div className="min-w-0 flex-1">
                    <p className="text-[13.5px] text-fg">{alarmSentence(alarm)}</p>
                    <p className="mt-0.5 flex flex-wrap gap-x-2 text-[12px] text-muted">
                      <span className="font-data">{alarm.namespace}</span>
                      <ObjectLink
                        clusterId={cluster.id}
                        namespace={alarm.namespace}
                        kind={alarm.kind}
                        title={kindTitle(alarm.kind)}
                        name={alarm.target}
                      />
                      {alarm.created_by ? <span>by {alarm.created_by}</span> : null}
                      <Age iso={alarm.created_at} />
                    </p>
                  </div>
                  <IconButton label="Change alarm" onClick={() => setEditing(alarm)}>
                    <Pencil aria-hidden="true" className="size-4" />
                  </IconButton>
                  <IconButton label="Delete alarm" tone="danger" onClick={() => void removeAlarm(alarm)}>
                    <Trash2 aria-hidden="true" className="size-4" />
                  </IconButton>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="card min-w-0 overflow-hidden" aria-label="Silences">
          <header className="border-b border-line-soft px-5 pt-4 pb-3.5">
            <h2 className="text-[16px] font-bold text-fg">Silences</h2>
            <p className="mt-0.5 text-[13px] text-muted">
              Active and scheduled silences pinned to a namespace your grant covers.
            </p>
          </header>
          {silences.error && !noAlertmanager ? (
            <div className="px-5 py-4">
              <Notice tone="warn">{errorMessage(silences.error, 'Could not read the silences.')}</Notice>
            </div>
          ) : null}
          {silences.data && live.length === 0 ? (
            <p className="px-5 py-8 text-center text-[13px] text-muted">Nothing is silenced.</p>
          ) : null}
          {live.length > 0 ? (
            <ul className="divide-y divide-line-soft">
              {live.map((silence) => (
                <li key={silence.id} className="flex flex-wrap items-center gap-3 px-5 py-3">
                  <Pill tone={silence.state === 'active' ? 'accent' : 'idle'}>{silence.state}</Pill>
                  <div className="min-w-0 flex-1">
                    <p className="text-[13.5px] text-fg">{silence.comment || 'No reason given'}</p>
                    <p className="mt-0.5 truncate font-data text-[12px] text-muted" title={matcherText(silence)}>
                      {matcherText(silence)}
                    </p>
                    <p className="mt-0.5 text-[12px] text-faint">
                      {silence.created_by} · ends <Age iso={silence.ends_at} />
                    </p>
                  </div>
                  {silence.can_expire ? (
                    <Button type="button" size="sm" variant="danger" onClick={() => void endSilence(silence)}>
                      End now
                    </Button>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}
        </section>
      </div>
    </AppShell>
  )
}

function matcherText(silence: AlertSilence): string {
  return silence.matchers
    .map((m) => `${m.name}${m.isEqual === false ? '!' : ''}${m.isRegex ? '=~' : '='}"${m.value}"`)
    .join(', ')
}
