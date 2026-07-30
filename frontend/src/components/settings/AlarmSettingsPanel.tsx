import { useCallback, useEffect, useMemo, useState } from 'react'
import { BellRing, CheckCircle2, Pencil, Plus, Send, Trash2, XCircle } from 'lucide-react'
import {
  createAlarmChannel,
  createAlarmRule,
  deleteAlarmChannel,
  deleteAlarmRule,
  errorMessage,
  fetchAlarmChannels,
  fetchAlarmRules,
  testAlarmChannel,
  updateAlarmChannel,
  updateAlarmRule,
} from '../../api/client'
import type {
  AlarmChannel,
  AlarmChannelInput,
  AlarmChannelKind,
  AlarmRule,
  AlarmRuleInput,
  AlarmRuleList,
  AlarmSeverity,
  AlarmTrigger,
  Cluster,
} from '../../api/types'
import type { Tone } from '../../lib/status'
import { relativeAge } from '../../lib/time'
import {
  Button,
  Chip,
  Field,
  IconButton,
  Notice,
  Panel,
  Pill,
  Select,
  Sheet,
  TextArea,
  TextInput,
} from '../primitives'

/**
 * Alarms: where a cluster event or a refused action goes when somebody has to
 * know now.
 *
 * The panel is deliberately two lists rather than one. A channel is where a
 * message goes and is configured once — it holds the credential, and reads back
 * only as "a credential is stored". A rule is what is worth sending and is
 * configured many times. Splitting them is what lets anyone administering alarms
 * duplicate and tune rules without the PagerDuty routing key having to be read
 * back out to do it.
 */

const KIND_LABEL: Record<AlarmChannelKind, string> = {
  alertmanager: 'Alertmanager',
  slack: 'Slack',
  pagerduty: 'PagerDuty',
  servicenow: 'ServiceNow / ITSM',
  webhook: 'Webhook / SIEM',
}

/** What each destination expects, shown where the URL is typed. A correct
    address to the wrong path is the most common way one of these silently 404s. */
const KIND_HINT: Record<AlarmChannelKind, string> = {
  alertmanager:
    'The v2 alerts endpoint, usually https://alertmanager.example.com/api/v2/alerts. Alarms then route through your existing silences and on-call rotation rather than beside them.',
  slack: 'An incoming webhook URL. The URL is the credential, so it needs no token.',
  pagerduty:
    'https://events.pagerduty.com/v2/enqueue, with the integration’s Events API v2 routing key.',
  servicenow:
    'The Table API incident endpoint, usually https://your-instance.service-now.com/api/now/table/incident, with a user that may open one.',
  webhook:
    'Any endpoint that accepts JSON. It receives the signal itself rather than a vendor envelope, which is what a SIEM’s own parsers want.',
}

const SEVERITY_TONE: Record<AlarmSeverity, Tone> = {
  critical: 'bad',
  warning: 'warn',
  info: 'accent',
}

const EMPTY_CHANNEL: AlarmChannelInput = {
  name: '',
  kind: 'alertmanager',
  url: '',
  auth_mode: 'none',
  username: '',
  secret: '',
  headers: '',
  enabled: true,
}

/** The auth mode each destination usually wants, so the common case needs no
    choice at all. */
function defaultAuthFor(kind: AlarmChannelKind): AlarmChannelInput['auth_mode'] {
  if (kind === 'pagerduty') return 'key'
  if (kind === 'servicenow') return 'basic'
  return 'none'
}

export function AlarmSettingsPanel({ clusters }: { clusters: Cluster[] }) {
  const [channels, setChannels] = useState<AlarmChannel[]>([])
  const [rules, setRules] = useState<AlarmRuleList | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const [editingChannel, setEditingChannel] = useState<AlarmChannel | 'new' | null>(null)
  const [editingRule, setEditingRule] = useState<AlarmRule | 'new' | null>(null)
  // The verdict of the last Test, keyed by channel so two rows cannot claim it.
  const [tested, setTested] = useState<Record<number, { ok: boolean; message: string }>>({})
  const [testing, setTesting] = useState<number | null>(null)

  const load = useCallback(async () => {
    try {
      const [channelList, ruleList] = await Promise.all([fetchAlarmChannels(), fetchAlarmRules()])
      setChannels(channelList.channels)
      setRules(ruleList)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the alarm configuration.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const channelsByID = useMemo(
    () => new Map(channels.map((channel) => [channel.id, channel])),
    [channels],
  )

  async function runTest(channel: AlarmChannel) {
    setTesting(channel.id)
    try {
      const result = await testAlarmChannel(channel.id)
      setTested((current) => ({ ...current, [channel.id]: result }))
      // The attempt is recorded on the channel, so the health column has to
      // catch up with what just happened.
      void load()
    } catch (err) {
      setTested((current) => ({
        ...current,
        [channel.id]: { ok: false, message: errorMessage(err, 'The test could not be sent.') },
      }))
    } finally {
      setTesting(null)
    }
  }

  async function removeChannel(channel: AlarmChannel) {
    // Deleting a channel takes its rules with it, which is worth saying before
    // it happens rather than after.
    const dependants = (rules?.rules ?? []).filter((rule) => rule.channel_id === channel.id).length
    const warning = dependants
      ? `Delete “${channel.name}”? The ${dependants} rule${dependants === 1 ? '' : 's'} delivering to it will be deleted too.`
      : `Delete “${channel.name}”?`
    if (!window.confirm(warning)) return

    try {
      await deleteAlarmChannel(channel.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not delete the channel.'))
    }
  }

  async function removeRule(rule: AlarmRule) {
    if (!window.confirm(`Delete the rule “${rule.name}”?`)) return
    try {
      await deleteAlarmRule(rule.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not delete the rule.'))
    }
  }

  return (
    <>
      <Panel
        eyebrow="Alarms"
        title="Where alarms are delivered"
        description="A channel is one destination and holds its own credential. Alertmanager composes with a fleet that already routes alerts; a webhook receives the raw signal, which is what a SIEM wants."
        bodyClassName="flex flex-col"
        actions={
          <Button size="sm" onClick={() => setEditingChannel('new')}>
            <Plus aria-hidden="true" className="size-3.5" />
            Add channel
          </Button>
        }
      >
        {error ? (
          <div className="px-4 pt-4">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        {rules && !rules.dispatcher_running ? (
          <div className="px-4 pt-4">
            <Notice tone="warn">
              No alarm dispatcher is running on this server, so nothing configured here will be
              delivered. Rules can still be written; they will start firing when a server with the
              dispatcher enabled picks them up.
            </Notice>
          </div>
        ) : null}

        {loading ? <p className="px-4 py-6 text-[13px] text-muted">Loading…</p> : null}

        {!loading && channels.length === 0 ? (
          <p className="px-4 py-6 text-[13px] text-muted">
            No channels yet. Add one and a rule can start delivering to it.
          </p>
        ) : null}

        <ul className="divide-y divide-line-soft">
          {channels.map((channel) => {
            const verdict = tested[channel.id]
            return (
              <li key={channel.id} className="flex flex-wrap items-start gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[13.5px] font-medium text-fg">
                      {channel.name}
                    </span>
                    <Pill tone="accent">{KIND_LABEL[channel.kind]}</Pill>
                    {channel.enabled ? null : <Pill tone="idle">disabled</Pill>}
                    {channel.has_secret ? <Pill tone="ok">credential stored</Pill> : null}
                  </div>
                  <p className="mt-1 truncate font-mono text-[12px] text-muted">{channel.url}</p>

                  {/* Delivery health. An integration that quietly stopped working
                      is the failure mode that matters: nobody notices a page that
                      was never sent. */}
                  {channel.last_status ? (
                    <p
                      className={`mt-1 text-[12px] ${
                        channel.last_status === 'ok' ? 'text-muted' : 'text-danger'
                      }`}
                    >
                      Last attempt {relativeAge(channel.last_attempt_at)} ·{' '}
                      {channel.last_status === 'ok'
                        ? 'accepted'
                        : channel.last_message || 'failed'}
                    </p>
                  ) : null}

                  {verdict ? (
                    <p
                      className={`mt-1 flex items-baseline gap-1.5 text-[12px] ${
                        verdict.ok ? 'text-ok' : 'text-danger'
                      }`}
                    >
                      {verdict.ok ? (
                        <CheckCircle2 aria-hidden="true" className="size-3.5 translate-y-0.5" />
                      ) : (
                        <XCircle aria-hidden="true" className="size-3.5 translate-y-0.5" />
                      )}
                      {verdict.message}
                    </p>
                  ) : null}
                </div>

                <div className="flex shrink-0 items-center gap-1.5">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={testing === channel.id}
                    onClick={() => void runTest(channel)}
                  >
                    <Send aria-hidden="true" className="size-3.5" />
                    {testing === channel.id ? 'Sending…' : 'Test'}
                  </Button>
                  <IconButton label="Edit channel" onClick={() => setEditingChannel(channel)}>
                    <Pencil aria-hidden="true" className="size-4" />
                  </IconButton>
                  <IconButton label="Delete channel" onClick={() => void removeChannel(channel)}>
                    <Trash2 aria-hidden="true" className="size-4" />
                  </IconButton>
                </div>
              </li>
            )
          })}
        </ul>
      </Panel>

      <Panel
        eyebrow="Alarms"
        title="What is worth sending"
        description="Cluster events are read down the agent tunnel — nothing is polled until a cluster-event rule exists. Audit rules cover the half no cluster-side alerting can see: a request KubeMG refused never reached the API server, so no cluster has an event for it."
        bodyClassName="flex flex-col"
        actions={
          <Button
            size="sm"
            disabled={channels.length === 0}
            onClick={() => setEditingRule('new')}
          >
            <Plus aria-hidden="true" className="size-3.5" />
            Add rule
          </Button>
        }
      >
        {rules && rules.rules.length === 0 ? (
          <p className="px-4 py-6 text-[13px] text-muted">
            {channels.length === 0
              ? 'Add a channel first — a rule with nowhere to deliver looks like coverage and is not.'
              : 'No rules yet. Nothing is polled and nothing is delivered until one exists.'}
          </p>
        ) : null}

        <ul className="divide-y divide-line-soft">
          {(rules?.rules ?? []).map((rule) => (
            <li key={rule.id} className="flex flex-wrap items-start gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-[13.5px] font-medium text-fg">{rule.name}</span>
                  <Pill tone={SEVERITY_TONE[rule.severity]}>{rule.severity}</Pill>
                  {rule.enabled ? null : <Pill tone="idle">disabled</Pill>}
                </div>
                <p className="mt-1 text-[12.5px] leading-relaxed text-muted">
                  {describeRule(rule, clusters)} →{' '}
                  <span className="text-fg">
                    {channelsByID.get(rule.channel_id)?.name ?? 'a deleted channel'}
                  </span>
                </p>
                {rule.description ? (
                  <p className="mt-1 text-[12px] text-faint">{rule.description}</p>
                ) : null}
                {rule.fire_count > 0 ? (
                  <p className="mt-1 font-mono text-[11.5px] text-faint">
                    fired {rule.fire_count}× · last {relativeAge(rule.last_fired_at)}
                  </p>
                ) : null}
              </div>

              <div className="flex shrink-0 items-center gap-1.5">
                <IconButton label="Edit rule" onClick={() => setEditingRule(rule)}>
                  <Pencil aria-hidden="true" className="size-4" />
                </IconButton>
                <IconButton label="Delete rule" onClick={() => void removeRule(rule)}>
                  <Trash2 aria-hidden="true" className="size-4" />
                </IconButton>
              </div>
            </li>
          ))}
        </ul>
      </Panel>

      {editingChannel ? (
        <ChannelSheet
          channel={editingChannel === 'new' ? null : editingChannel}
          onClose={() => setEditingChannel(null)}
          onSaved={() => {
            setEditingChannel(null)
            void load()
          }}
        />
      ) : null}

      {editingRule && rules ? (
        <RuleSheet
          rule={editingRule === 'new' ? null : editingRule}
          channels={channels}
          clusters={clusters}
          vocabulary={rules}
          onClose={() => setEditingRule(null)}
          onSaved={() => {
            setEditingRule(null)
            void load()
          }}
        />
      ) : null}
    </>
  )
}

/** describeRule renders a rule's matchers as the sentence an operator wrote it
    to mean, so the list is readable without opening each one. */
function describeRule(rule: AlarmRule, clusters: Cluster[]): string {
  const where =
    rule.cluster_id === 0
      ? 'any cluster'
      : (clusters.find((cluster) => cluster.id === rule.cluster_id)?.name ??
        `cluster ${rule.cluster_id}`)
  const scope = rule.namespaces ? `${where}/${rule.namespaces}` : where

  if (rule.trigger === 'cluster_event') {
    const reasons = rule.event_reasons || 'any reason'
    const type = rule.event_type ? `${rule.event_type} events` : 'events'
    return `${type} (${reasons}) on ${scope}`
  }

  const verbs = rule.verbs || 'any action'
  const outcome = rule.denied_only ? 'refused' : 'audited'
  const status = rule.min_status ? ` at or above HTTP ${rule.min_status}` : ''
  return `${outcome} ${verbs} on ${scope}${status}`
}

/* -------------------------------------------------------------- channel --- */

function ChannelSheet({
  channel,
  onClose,
  onSaved,
}: {
  channel: AlarmChannel | null
  onClose: () => void
  onSaved: () => void
}) {
  const [draft, setDraft] = useState<AlarmChannelInput>(
    channel
      ? {
          name: channel.name,
          kind: channel.kind,
          url: channel.url,
          auth_mode: channel.auth_mode,
          username: channel.username ?? '',
          // Always blank: the stored credential is never read back, and leaving
          // it empty is what keeps it.
          secret: '',
          headers: channel.headers ?? '',
          enabled: channel.enabled,
        }
      : EMPTY_CHANNEL,
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function set<K extends keyof AlarmChannelInput>(key: K, value: AlarmChannelInput[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const payload: AlarmChannelInput = {
        ...draft,
        name: draft.name.trim(),
        url: draft.url.trim(),
        // An empty secret means "keep the stored one", so it is omitted rather
        // than sent blank.
        secret: draft.secret?.trim() ? draft.secret.trim() : undefined,
      }
      if (channel) {
        await updateAlarmChannel(channel.id, payload)
      } else {
        await createAlarmChannel(payload)
      }
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the channel.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      width="lg"
      eyebrow="Alarms"
      title={channel ? `Edit ${channel.name}` : 'New alarm channel'}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Save channel'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Field label="Name" htmlFor="channel_name" hint="What a rule points at.">
          <TextInput
            id="channel_name"
            value={draft.name}
            onChange={(event) => set('name', event.target.value)}
            placeholder="on-call"
          />
        </Field>

        <Field label="Destination" htmlFor="channel_kind" hint={KIND_HINT[draft.kind]}>
          <Select
            id="channel_kind"
            value={draft.kind}
            onChange={(event) => {
              const kind = event.target.value as AlarmChannelKind
              setDraft((current) => ({
                ...current,
                kind,
                // Each destination takes its credential a different way, so
                // switching kind resets the mode to the one that works.
                auth_mode: defaultAuthFor(kind),
              }))
            }}
          >
            {(Object.keys(KIND_LABEL) as AlarmChannelKind[]).map((kind) => (
              <option key={kind} value={kind}>
                {KIND_LABEL[kind]}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="Endpoint URL"
          htmlFor="channel_url"
          hint="Dialled from KubeMG, so it has to be reachable from here. An internal address is the usual case."
        >
          <TextInput
            id="channel_url"
            className="font-mono text-[12.5px]"
            value={draft.url}
            onChange={(event) => set('url', event.target.value)}
            placeholder="https://alertmanager.example.com/api/v2/alerts"
          />
        </Field>

        <Field
          label="Authentication"
          htmlFor="channel_auth"
          hint={
            draft.kind === 'pagerduty'
              ? 'PagerDuty has no header for its routing key — it rides in the payload.'
              : 'A Slack webhook needs none: the URL is the credential.'
          }
        >
          <Select
            id="channel_auth"
            value={draft.auth_mode}
            onChange={(event) =>
              set('auth_mode', event.target.value as AlarmChannelInput['auth_mode'])
            }
          >
            <option value="none">None</option>
            <option value="bearer">Bearer token</option>
            <option value="basic">Basic auth</option>
            <option value="key">Routing key (in payload)</option>
          </Select>
        </Field>

        {draft.auth_mode === 'basic' ? (
          <Field label="Username" htmlFor="channel_username">
            <TextInput
              id="channel_username"
              value={draft.username ?? ''}
              onChange={(event) => set('username', event.target.value)}
            />
          </Field>
        ) : null}

        {draft.auth_mode !== 'none' ? (
          <Field
            label={draft.auth_mode === 'key' ? 'Routing key' : 'Token or password'}
            htmlFor="channel_secret"
            hint={
              channel?.has_secret
                ? 'A credential is stored. Leave this empty to keep it — it is never read back out.'
                : 'Stored and never returned by the API.'
            }
          >
            <TextInput
              id="channel_secret"
              type="password"
              autoComplete="new-password"
              className="font-mono text-[12.5px]"
              value={draft.secret ?? ''}
              onChange={(event) => set('secret', event.target.value)}
              placeholder={channel?.has_secret ? '••••••••' : ''}
            />
          </Field>
        ) : null}

        <Field
          label="Extra headers"
          htmlFor="channel_headers"
          hint={`Optional JSON object, for a SIEM's tenant id or an API version. Authorization and Content-Type are set by KubeMG and cannot be overridden here.`}
        >
          <TextArea
            id="channel_headers"
            rows={2}
            value={draft.headers ?? ''}
            onChange={(event) => set('headers', event.target.value)}
            placeholder='{"X-Tenant":"prod"}'
          />
        </Field>

        <label className="flex items-center gap-2.5">
          <input
            type="checkbox"
            className="size-4 accent-[var(--color-accent)]"
            checked={draft.enabled ?? true}
            onChange={(event) => set('enabled', event.target.checked)}
          />
          <span className="text-[13px] text-fg">Enabled</span>
        </label>
      </div>
    </Sheet>
  )
}

/* ----------------------------------------------------------------- rule --- */

function RuleSheet({
  rule,
  channels,
  clusters,
  vocabulary,
  onClose,
  onSaved,
}: {
  rule: AlarmRule | null
  channels: AlarmChannel[]
  clusters: Cluster[]
  vocabulary: AlarmRuleList
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(rule?.name ?? '')
  const [description, setDescription] = useState(rule?.description ?? '')
  const [channelID, setChannelID] = useState(rule?.channel_id ?? channels[0]?.id ?? 0)
  const [enabled, setEnabled] = useState(rule?.enabled ?? true)
  const [trigger, setTrigger] = useState<AlarmTrigger>(rule?.trigger ?? 'cluster_event')
  const [clusterID, setClusterID] = useState(rule?.cluster_id ?? 0)
  const [namespaces, setNamespaces] = useState(rule?.namespaces ?? '')
  const [reasons, setReasons] = useState<string[]>(splitList(rule?.event_reasons))
  const [eventType, setEventType] = useState(rule?.event_type ?? 'Warning')
  const [verbs, setVerbs] = useState<string[]>(splitList(rule?.verbs))
  const [deniedOnly, setDeniedOnly] = useState(rule?.denied_only ?? true)
  const [minStatus, setMinStatus] = useState(rule?.min_status ? String(rule.min_status) : '')
  const [severity, setSeverity] = useState<AlarmSeverity>(rule?.severity ?? 'warning')
  const [cooloff, setCooloff] = useState(rule?.cooloff_seconds ? String(rule.cooloff_seconds) : '')

  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const payload: AlarmRuleInput = {
        name: name.trim(),
        description: description.trim(),
        channel_id: channelID,
        enabled,
        trigger,
        cluster_id: clusterID,
        namespaces: splitList(namespaces),
        event_reasons: trigger === 'cluster_event' ? reasons : [],
        event_type: trigger === 'cluster_event' ? eventType : '',
        verbs: trigger === 'audit' ? verbs : [],
        denied_only: trigger === 'audit' ? deniedOnly : false,
        min_status: trigger === 'audit' && minStatus ? Number(minStatus) : 0,
        severity,
        cooloff_seconds: cooloff ? Number(cooloff) : 0,
      }
      if (rule) {
        await updateAlarmRule(rule.id, payload)
      } else {
        await createAlarmRule(payload)
      }
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save the rule.'))
    } finally {
      setBusy(false)
    }
  }

  function toggleReason(reason: string) {
    setReasons((current) =>
      current.includes(reason)
        ? current.filter((entry) => entry !== reason)
        : [...current, reason],
    )
  }

  function toggleVerb(verb: string) {
    setVerbs((current) =>
      current.includes(verb) ? current.filter((entry) => entry !== verb) : [...current, verb],
    )
  }

  return (
    <Sheet
      width="lg"
      eyebrow="Alarms"
      title={rule ? `Edit ${rule.name}` : 'New alarm rule'}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Save rule'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Field label="Name" htmlFor="rule_name">
          <TextInput
            id="rule_name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Pods OOMKilled in production"
          />
        </Field>

        <Field
          label="Why this rule exists"
          htmlFor="rule_description"
          hint="The field that stops a rule set from becoming undeletable six months later."
        >
          <TextArea
            id="rule_description"
            rows={2}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>

        <Field label="Deliver to" htmlFor="rule_channel">
          <Select
            id="rule_channel"
            value={channelID}
            onChange={(event) => setChannelID(Number(event.target.value))}
          >
            {channels.map((channel) => (
              <option key={channel.id} value={channel.id}>
                {channel.name} · {KIND_LABEL[channel.kind]}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="Watch"
          htmlFor="rule_trigger"
          hint={
            trigger === 'cluster_event'
              ? vocabulary.cluster_events_available
                ? 'Kubernetes Events read down the agent tunnel, once a minute per cluster a rule covers.'
                : 'This server has no proxy, so there is no tunnel to read cluster events down. A rule here would never fire.'
              : 'KubeMG’s own audit records — including the refusals no cluster ever sees.'
          }
        >
          <Select
            id="rule_trigger"
            value={trigger}
            onChange={(event) => setTrigger(event.target.value as AlarmTrigger)}
          >
            <option value="cluster_event">Cluster events</option>
            <option value="audit">KubeMG audit records</option>
          </Select>
        </Field>

        <Field
          label="Cluster"
          htmlFor="rule_cluster"
          hint="Leaving this on every cluster covers ones registered later too, which a per-cluster rule could never keep up with."
        >
          <Select
            id="rule_cluster"
            value={clusterID}
            onChange={(event) => setClusterID(Number(event.target.value))}
          >
            <option value={0}>Every cluster</option>
            {clusters.map((cluster) => (
              <option key={cluster.id} value={cluster.id}>
                {cluster.name}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="Namespaces"
          htmlFor="rule_namespaces"
          hint="Comma-separated. Empty means every namespace."
        >
          <TextInput
            id="rule_namespaces"
            className="font-mono text-[12.5px]"
            value={namespaces}
            onChange={(event) => setNamespaces(event.target.value)}
            placeholder="payments,checkout"
          />
        </Field>

        {trigger === 'cluster_event' ? (
          <>
            <Field label="Event type" htmlFor="rule_event_type">
              <Select
                id="rule_event_type"
                value={eventType}
                onChange={(event) => setEventType(event.target.value)}
              >
                <option value="Warning">Warning</option>
                <option value="Normal">Normal</option>
                <option value="">Either</option>
              </Select>
            </Field>

            <div className="flex flex-col gap-2">
              <p className="label">Reasons</p>
              <div className="flex flex-wrap gap-2">
                {vocabulary.suggested_reasons.map((reason) => (
                  <Chip
                    key={reason}
                    active={reasons.includes(reason)}
                    onClick={() => toggleReason(reason)}
                  >
                    <span className="font-mono text-[12.5px]">{reason}</span>
                  </Chip>
                ))}
              </div>
              <p className="text-[12px] leading-snug text-muted">
                An event’s reason comes from whichever controller wrote it, so these are
                suggestions rather than the whole set. A rule matching neither a type nor a reason
                would fire on everything the cluster says, and is refused.
              </p>
            </div>
          </>
        ) : (
          <>
            <div className="flex flex-col gap-2">
              <p className="label">Actions</p>
              <div className="flex flex-wrap gap-2">
                {AUDIT_VERBS.map((verb) => (
                  <Chip key={verb} active={verbs.includes(verb)} onClick={() => toggleVerb(verb)}>
                    <span className="font-mono text-[12.5px]">{verb}</span>
                  </Chip>
                ))}
              </div>
              <p className="text-[12px] leading-snug text-muted">
                None selected means any action.
              </p>
            </div>

            <label className="flex items-start gap-2.5">
              <input
                type="checkbox"
                className="mt-0.5 size-4 accent-[var(--color-accent)]"
                checked={deniedOnly}
                onChange={(event) => setDeniedOnly(event.target.checked)}
              />
              <span className="min-w-0">
                <span className="text-[13px] text-fg">Only when KubeMG refused it</span>
                <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                  A refused request never reached the API server, so no cluster-side alerting can
                  see it. This is the setting most audit rules want.
                </span>
              </span>
            </label>

            <Field
              label="Minimum HTTP status"
              htmlFor="rule_min_status"
              hint="Optional. 500 narrows to errors rather than refusals."
            >
              <TextInput
                id="rule_min_status"
                type="number"
                min={100}
                max={599}
                className="max-w-32 font-mono text-[12.5px]"
                value={minStatus}
                onChange={(event) => setMinStatus(event.target.value)}
              />
            </Field>
          </>
        )}

        <Field label="Severity" htmlFor="rule_severity">
          <Select
            id="rule_severity"
            value={severity}
            onChange={(event) => setSeverity(event.target.value as AlarmSeverity)}
          >
            {vocabulary.severities.map((entry) => (
              <option key={entry} value={entry}>
                {entry}
              </option>
            ))}
          </Select>
        </Field>

        <Field
          label="Cool-off (seconds)"
          htmlFor="rule_cooloff"
          hint="How long the same problem is suppressed after it fires. Empty takes 5 minutes. A crash loop re-emits its event every few seconds, and a channel that pages that often gets muted by its recipient."
        >
          <TextInput
            id="rule_cooloff"
            type="number"
            min={0}
            max={86400}
            className="max-w-32 font-mono text-[12.5px]"
            value={cooloff}
            onChange={(event) => setCooloff(event.target.value)}
            placeholder="300"
          />
        </Field>

        <label className="flex items-center gap-2.5">
          <input
            type="checkbox"
            className="size-4 accent-[var(--color-accent)]"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
          />
          <span className="text-[13px] text-fg">Enabled</span>
        </label>

        <p className="flex items-baseline gap-1.5 text-[12px] text-muted">
          <BellRing aria-hidden="true" className="size-3.5 translate-y-0.5 shrink-0" />
          Use Test on the channel to prove the endpoint accepts KubeMG’s payload before relying on
          this rule.
        </p>
      </div>
    </Sheet>
  )
}

/** The verbs an audit rule can match, matching what the server accepts. */
const AUDIT_VERBS = [
  'get',
  'list',
  'watch',
  'create',
  'update',
  'patch',
  'delete',
  'log',
  'exec',
  'attach',
  'portforward',
  'replay',
  'recording-delete',
]

function splitList(raw: string | undefined): string[] {
  if (!raw) return []
  return raw
    .split(',')
    .map((entry) => entry.trim())
    .filter(Boolean)
}
