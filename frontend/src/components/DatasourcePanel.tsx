import { useCallback, useEffect, useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import {
  Activity,
  Check,
  ExternalLink,
  Pencil,
  Plug,
  RefreshCw,
  Search,
  ScrollText,
  Trash2,
  X,
} from 'lucide-react'
import {
  checkDatasource,
  deleteDatasource,
  discoverDatasources,
  errorMessage,
  fetchObservability,
  saveDatasource,
  testDatasource,
} from '../api/client'
import type {
  Cluster,
  DatasourceAccess,
  DatasourceAuth,
  DatasourceCandidate,
  DatasourceCheck,
  DatasourceInput,
  DatasourceKind,
  DatasourceProvider,
  ObservabilityResponse,
  ObservabilitySource,
} from '../api/types'
import {
  DATASOURCE_KINDS,
  KIND_LABEL,
  KIND_PURPOSE,
  PROVIDERS,
  providersFor,
  sourceStateLabel,
  sourceTone,
} from '../lib/datasources'
import { datasourceUILabel } from '../lib/consoles'
import { relativeAge } from '../lib/time'
import {
  Button,
  Field,
  IconButton,
  Notice,
  Panel,
  Pill,
  Segmented,
  Select,
  Sheet,
  TextInput,
} from './primitives'

/**
 * Where a cluster's metrics and logs come from.
 *
 * This sits on the cluster because that is what it describes: KubeMG's live
 * meters read the cluster's own Metrics API, which keeps about two minutes, so
 * anything asking "since when" needs a series backend and each cluster has its
 * own. Connecting one is two decisions — which product, and how KubeMG reaches
 * it — and the second one is the interesting half: an in-cluster backend is
 * reached down the agent tunnel through the API server's Service proxy, so
 * nothing has to be exposed and the read is impersonated and audited like any
 * other. Everything else on this screen exists to get those two right, which is
 * why the cluster is scanned for backends it is already running rather than
 * making someone go and read a Service list.
 */
export function DatasourcePanel({
  cluster,
  eyebrow = 'Observability',
  className,
}: {
  cluster: Cluster
  eyebrow?: string
  className?: string
}) {
  const [state, setState] = useState<ObservabilityResponse | null>(null)
  const [candidates, setCandidates] = useState<DatasourceCandidate[] | null>(null)
  const [scanning, setScanning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [busyKind, setBusyKind] = useState<DatasourceKind | null>(null)
  const [editing, setEditing] = useState<DatasourceKind | null>(null)

  const clusterId = cluster.id

  const load = useCallback(async () => {
    try {
      setState(await fetchObservability(clusterId))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load this cluster’s datasources.'))
    } finally {
      setLoading(false)
    }
  }, [clusterId])

  useEffect(() => {
    void load()
  }, [load])

  const viaTunnel = state?.agent_attached ?? false

  const scan = useCallback(async () => {
    setScanning(true)
    setError(null)
    try {
      setCandidates(await discoverDatasources(clusterId))
    } catch (err) {
      setCandidates(null)
      setError(errorMessage(err, 'Could not scan this cluster.'))
    } finally {
      setScanning(false)
    }
  }, [clusterId])

  async function recheck(kind: DatasourceKind) {
    setBusyKind(kind)
    setError(null)
    try {
      await checkDatasource(clusterId, kind)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not check the datasource.'))
    } finally {
      setBusyKind(null)
    }
  }

  async function remove(kind: DatasourceKind) {
    setBusyKind(kind)
    setError(null)
    try {
      await deleteDatasource(clusterId, kind)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not remove the datasource.'))
    } finally {
      setBusyKind(null)
    }
  }

  const sources = useMemo(() => {
    const byKind = new Map<DatasourceKind, ObservabilitySource>()
    for (const source of state?.sources ?? []) byKind.set(source.kind, source)
    return byKind
  }, [state])

  const editable = state?.editable ?? false

  return (
    <Panel
      eyebrow={eyebrow}
      title="Metrics & logs sources"
      description="Where this cluster's history lives. KubeMG's live meters read the cluster's own Metrics API, which keeps about two minutes — anything over time comes from here."
      className={className}
      actions={
        editable && viaTunnel ? (
          <Button onClick={scan} disabled={scanning}>
            <Search aria-hidden="true" className={`size-4 ${scanning ? 'animate-pulse' : ''}`} />
            {scanning ? 'Scanning…' : 'Scan cluster'}
          </Button>
        ) : null
      }
    >
      <div className="flex flex-col">
        {error ? (
          <div className="p-4 pb-0">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        {loading ? (
          <p className="px-4 py-6 text-[13px] text-muted">Loading…</p>
        ) : (
          DATASOURCE_KINDS.map((kind) => (
            <SourceRow
              key={kind}
              kind={kind}
              source={sources.get(kind)}
              editable={editable}
              busy={busyKind === kind}
              onEdit={() => setEditing(kind)}
              onCheck={() => recheck(kind)}
              onRemove={() => remove(kind)}
            />
          ))
        )}

        {candidates ? (
          <Discovered
            candidates={candidates}
            onDismiss={() => setCandidates(null)}
            onPick={(candidate) => setEditing(candidate.kind)}
            picked={editing}
          />
        ) : null}

        {!loading && !viaTunnel && editable ? (
          <p className="border-t border-line-soft px-4 py-3 text-[12px] leading-relaxed text-muted">
            {cluster.connection_mode === 'agent'
              ? 'No agent is attached right now, so KubeMG cannot scan this cluster or reach a backend inside it. An external address still works.'
              : 'This cluster is registered in direct mode and has no tunnel, so a datasource has to be one KubeMG can dial itself.'}
          </p>
        ) : null}
      </div>

      {editing ? (
        <DatasourceSheet
          cluster={cluster}
          kind={editing}
          source={sources.get(editing) ?? null}
          candidates={(candidates ?? []).filter((candidate) => candidate.kind === editing)}
          canUseTunnel={viaTunnel}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
          }}
        />
      ) : null}
    </Panel>
  )
}

const KIND_ICON = { metrics: Activity, logs: ScrollText } as const

/** SourceRow is one kind of datasource: connected, or the offer to connect it. */
function SourceRow({
  kind,
  source,
  editable,
  busy,
  onEdit,
  onCheck,
  onRemove,
}: {
  kind: DatasourceKind
  source: ObservabilitySource | undefined
  editable: boolean
  busy: boolean
  onEdit: () => void
  onCheck: () => void
  onRemove: () => void
}) {
  const Icon = KIND_ICON[kind]

  return (
    <div className="flex flex-wrap items-center gap-3 border-t border-line-soft px-4 py-3 first:border-t-0">
      <Icon aria-hidden="true" className="size-4 shrink-0 text-muted" />

      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[13.5px] font-medium text-fg">{KIND_LABEL[kind]}</span>
          {source ? (
            <>
              <span className="font-mono text-[12.5px] text-muted">{source.provider_label}</span>
              <Pill tone={sourceTone(source)} title={source.last_message}>
                {sourceStateLabel(source)}
              </Pill>
              {source.detected_version ? (
                <span className="font-mono text-[11.5px] text-faint">{source.detected_version}</span>
              ) : null}
            </>
          ) : (
            <Pill tone="idle" dot={false}>
              Not connected
            </Pill>
          )}
        </div>

        {source?.ui_url ? (
          <a
            href={source.ui_url}
            target="_blank"
            rel="noreferrer noopener"
            className="mt-1 inline-flex items-center gap-1.5 text-[12px] text-muted transition-colors hover:text-fg"
            title={source.ui_url}
          >
            <ExternalLink aria-hidden="true" className="size-3.5" />
            Open {datasourceUILabel(source.provider)}
          </a>
        ) : null}

        {source ? (
          <p className="mt-1 truncate font-mono text-[12px] text-faint" title={source.endpoint}>
            {source.endpoint}
            {source.access_mode === 'in-cluster' ? ' · via tunnel' : ' · dialled from KubeMG'}
            {source.last_checked_at ? ` · checked ${relativeAge(source.last_checked_at)}` : null}
          </p>
        ) : (
          <p className="mt-1 max-w-2xl text-[12px] leading-relaxed text-muted">
            {KIND_PURPOSE[kind]}
          </p>
        )}

        {source && source.last_status === 'unhealthy' && source.last_message ? (
          <p className="mt-1.5 text-[12px] leading-relaxed text-warn">{source.last_message}</p>
        ) : null}
      </div>

      {editable ? (
        <div className="flex shrink-0 items-center gap-2">
          {source ? (
            <>
              <IconButton label={`Check the ${kind} source`} onClick={onCheck} disabled={busy}>
                <RefreshCw aria-hidden="true" className={`size-3.5 ${busy ? 'animate-spin' : ''}`} />
              </IconButton>
              <IconButton label={`Edit the ${kind} source`} onClick={onEdit} disabled={busy}>
                <Pencil aria-hidden="true" className="size-3.5" />
              </IconButton>
              <IconButton
                label={`Remove the ${kind} source`}
                tone="danger"
                onClick={onRemove}
                disabled={busy}
              >
                <Trash2 aria-hidden="true" className="size-3.5" />
              </IconButton>
            </>
          ) : (
            <Button onClick={onEdit}>
              <Plug aria-hidden="true" className="size-4" />
              Connect
            </Button>
          )}
        </div>
      ) : null}
    </div>
  )
}

/**
 * Discovered is what the cluster is already running. A match is a suggestion and
 * never a configuration — it opens the form filled in, and someone still has to
 * look at it and save.
 */
function Discovered({
  candidates,
  onDismiss,
  onPick,
  picked,
}: {
  candidates: DatasourceCandidate[]
  onDismiss: () => void
  onPick: (candidate: DatasourceCandidate) => void
  picked: DatasourceKind | null
}) {
  return (
    <div className="border-t border-line-soft bg-raised/40">
      <div className="flex items-center gap-2 px-4 py-2.5">
        <span className="label">Found in this cluster</span>
        <span className="ml-auto">
          <IconButton label="Dismiss the scan results" onClick={onDismiss}>
            <X aria-hidden="true" className="size-3.5" />
          </IconButton>
        </span>
      </div>

      {candidates.length === 0 ? (
        <p className="px-4 pb-3 text-[12.5px] leading-relaxed text-muted">
          Nothing recognisable is running here. KubeMG looks for VictoriaMetrics, Prometheus,
          Thanos, Mimir, VictoriaLogs and Loki by Service name and port — a backend behind an
          unusual name still works, entered by hand.
        </p>
      ) : (
        <ul>
          {candidates.map((candidate) => (
            <li
              key={`${candidate.service_namespace}/${candidate.service_name}/${candidate.service_port}`}
              className="flex flex-wrap items-center gap-3 px-4 py-2.5"
            >
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-[13px] text-fg">
                    {candidate.service_namespace}/{candidate.service_name}:{candidate.service_port}
                  </span>
                  <Pill tone={candidate.score > 1 ? 'ok' : 'warn'} dot={false}>
                    {PROVIDERS[candidate.provider].label}
                  </Pill>
                  <span className="label">{KIND_LABEL[candidate.kind]}</span>
                </span>
                <span className="mt-0.5 block text-[12px] leading-snug text-muted">
                  {candidate.reason}
                </span>
              </span>
              <Button onClick={() => onPick(candidate)} disabled={picked === candidate.kind}>
                Use this
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Draft is the form's own state: every field is a string until it is saved. */
interface Draft {
  provider: DatasourceProvider
  access_mode: DatasourceAccess
  url: string
  service_namespace: string
  service_name: string
  service_port: string
  service_scheme: 'http' | 'https'
  path_prefix: string
  auth_mode: DatasourceAuth
  username: string
  credential: string
  insecure_skip_verify: boolean
  enabled: boolean
  grafana_datasource: string
}

function blankDraft(kind: DatasourceKind, canUseTunnel: boolean): Draft {
  const provider = providersFor(kind)[0]
  return {
    provider,
    access_mode: canUseTunnel ? 'in-cluster' : 'direct',
    url: '',
    service_namespace: '',
    service_name: '',
    service_port: PROVIDERS[provider].defaultPort,
    service_scheme: 'http',
    path_prefix: PROVIDERS[provider].defaultPrefix,
    auth_mode: 'none',
    username: '',
    credential: '',
    insecure_skip_verify: false,
    enabled: true,
    grafana_datasource: '',
  }
}

function draftFrom(source: ObservabilitySource): Draft {
  return {
    provider: source.provider,
    access_mode: source.access_mode,
    url: source.url ?? '',
    service_namespace: source.service_namespace ?? '',
    service_name: source.service_name ?? '',
    service_port: source.service_port ?? '',
    service_scheme: source.service_scheme === 'https' ? 'https' : 'http',
    path_prefix: source.path_prefix ?? '',
    auth_mode: source.auth_mode,
    username: source.username ?? '',
    credential: '',
    insecure_skip_verify: source.insecure_skip_verify,
    enabled: source.enabled,
    grafana_datasource: source.grafana_datasource ?? '',
  }
}

/**
 * toInput leaves the credential out when the field was left empty and one is
 * already stored: editing a port must not mean re-typing a token, and an empty
 * field is far more likely to mean "leave it alone" than "clear it".
 */
function toInput(draft: Draft, hasStoredCredential: boolean): DatasourceInput {
  const shared = {
    provider: draft.provider,
    access_mode: draft.access_mode,
    path_prefix: draft.path_prefix.trim(),
    auth_mode: draft.auth_mode,
    username: draft.auth_mode === 'basic' ? draft.username.trim() : '',
    enabled: draft.enabled,
    grafana_datasource: draft.grafana_datasource.trim(),
  }

  const credential =
    draft.credential === '' && hasStoredCredential ? {} : { credential: draft.credential }

  if (draft.access_mode === 'in-cluster') {
    return {
      ...shared,
      ...credential,
      service_namespace: draft.service_namespace.trim(),
      service_name: draft.service_name.trim(),
      service_port: draft.service_port.trim(),
      service_scheme: draft.service_scheme,
    }
  }
  return {
    ...shared,
    ...credential,
    url: draft.url.trim(),
    insecure_skip_verify: draft.insecure_skip_verify,
  }
}

/**
 * DatasourceSheet is the editor. It can check a draft nobody has saved yet,
 * which is the point: an address that is going to 404 should say so while the
 * operator is still looking at the field holding it, not weeks later as an
 * empty chart.
 */
export function DatasourceSheet({
  cluster,
  kind,
  source,
  candidates,
  canUseTunnel,
  onSaved,
  onClose,
}: {
  cluster: Cluster
  kind: DatasourceKind
  source: ObservabilitySource | null
  candidates: DatasourceCandidate[]
  canUseTunnel: boolean
  onSaved: () => void | Promise<void>
  onClose: () => void
}) {
  const [draft, setDraft] = useState<Draft>(() =>
    source ? draftFrom(source) : blankDraft(kind, canUseTunnel),
  )
  const [check, setCheck] = useState<DatasourceCheck | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)

  const hasStoredCredential = source?.has_credential ?? false

  function update<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => ({ ...current, [key]: value }))
    setCheck(null)
  }

  // Changing the product moves the defaults with it, but never overwrites
  // something already typed: a port someone entered is an answer, not a leftover.
  function pickProvider(provider: DatasourceProvider) {
    setDraft((current) => ({
      ...current,
      provider,
      service_port:
        current.service_port === '' || current.service_port === PROVIDERS[current.provider].defaultPort
          ? PROVIDERS[provider].defaultPort
          : current.service_port,
      path_prefix:
        current.path_prefix === '' || current.path_prefix === PROVIDERS[current.provider].defaultPrefix
          ? PROVIDERS[provider].defaultPrefix
          : current.path_prefix,
    }))
    setCheck(null)
  }

  function applyCandidate(candidate: DatasourceCandidate) {
    setDraft((current) => ({
      ...current,
      provider: candidate.provider,
      access_mode: 'in-cluster',
      service_namespace: candidate.service_namespace,
      service_name: candidate.service_name,
      service_port: candidate.service_port,
      service_scheme: candidate.service_scheme === 'https' ? 'https' : 'http',
      path_prefix: candidate.path_prefix ?? '',
    }))
    setCheck(null)
  }

  async function runTest() {
    setTesting(true)
    setError(null)
    try {
      setCheck(await testDatasource(cluster.id, kind, toInput(draft, hasStoredCredential)))
    } catch (err) {
      setCheck(null)
      setError(errorMessage(err, 'Could not check that datasource.'))
    } finally {
      setTesting(false)
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await saveDatasource(cluster.id, kind, toInput(draft, hasStoredCredential))
      await onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save that datasource.'))
    } finally {
      setBusy(false)
    }
  }

  const info = PROVIDERS[draft.provider]
  const inCluster = draft.access_mode === 'in-cluster'

  return (
    <Sheet
      eyebrow={`${KIND_LABEL[kind]} source`}
      title={
        <>
          {source ? 'Edit' : 'Connect'} {KIND_LABEL[kind].toLowerCase()} for{' '}
          <span className="font-mono text-accent">{cluster.name}</span>
        </>
      }
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="button" onClick={runTest} disabled={testing || busy}>
            <RefreshCw aria-hidden="true" className={`size-4 ${testing ? 'animate-spin' : ''}`} />
            {testing ? 'Checking…' : 'Check connection'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save source'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {check ? (
        <Notice tone={check.reachable ? 'ok' : 'warn'}>
          {check.reachable ? (
            <Check aria-hidden="true" className="mr-1.5 -mt-0.5 inline size-3.5" />
          ) : null}
          {check.message}
          <span className="mt-1 block font-mono text-[11.5px] opacity-80">
            {check.endpoint}
            {check.path}
          </span>
        </Notice>
      ) : null}

      {candidates.length > 0 ? (
        <Field
          label="Found in this cluster"
          htmlFor="candidates"
          hint="Fills the form in; nothing is saved until you do."
        >
          <div className="flex flex-wrap gap-2">
            {candidates.map((candidate, index) => (
              <button
                id={index === 0 ? 'candidates' : undefined}
                key={`${candidate.service_namespace}/${candidate.service_name}`}
                type="button"
                onClick={() => applyCandidate(candidate)}
                className="rounded-control border border-line bg-surface px-2.5 py-1.5 text-left font-mono text-[12px] text-muted transition-colors hover:bg-raised hover:text-fg"
              >
                {candidate.service_namespace}/{candidate.service_name}:{candidate.service_port}
              </button>
            ))}
          </div>
        </Field>
      ) : null}

      <Field label="Product" htmlFor="provider" hint={info.hint}>
        <Select
          id="provider"
          value={draft.provider}
          onChange={(event) => pickProvider(event.target.value as DatasourceProvider)}
        >
          {providersFor(kind).map((provider) => (
            <option key={provider} value={provider}>
              {PROVIDERS[provider].label}
            </option>
          ))}
        </Select>
      </Field>

      <Field
        label="How KubeMG reaches it"
        htmlFor="access_mode"
        hint={
          inCluster
            ? 'Through the agent tunnel, by asking the API server to proxy to the Service. Nothing has to be exposed, and the read is impersonated and audited like any other.'
            : 'KubeMG dials the address itself, so it has to be routable from this server.'
        }
      >
        <Segmented<DatasourceAccess>
          ariaLabel="Access mode"
          id="access_mode"
          value={draft.access_mode}
          onChange={(next) => update('access_mode', next)}
          options={[
            { value: 'in-cluster', label: 'Inside the cluster' },
            { value: 'direct', label: 'External address' },
          ]}
        />
      </Field>

      {inCluster && !canUseTunnel ? (
        <Notice tone="warn">
          {cluster.connection_mode === 'agent'
            ? 'No agent is attached right now, so this cannot be checked until one connects. It can still be saved.'
            : 'This cluster is registered in direct mode, so it has no tunnel — an in-cluster source cannot be reached and will be refused. Give the external address instead.'}
        </Notice>
      ) : null}

      {inCluster ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Namespace" htmlFor="service_namespace">
              <TextInput
                id="service_namespace"
                required
                className="font-mono text-[12.5px]"
                placeholder="monitoring"
                value={draft.service_namespace}
                onChange={(event) => update('service_namespace', event.target.value)}
              />
            </Field>
            <Field label="Service" htmlFor="service_name">
              <TextInput
                id="service_name"
                required
                className="font-mono text-[12.5px]"
                placeholder="prometheus-server"
                value={draft.service_name}
                onChange={(event) => update('service_name', event.target.value)}
              />
            </Field>
            <Field label="Port" htmlFor="service_port">
              <TextInput
                id="service_port"
                required
                className="font-mono text-[12.5px]"
                placeholder={info.defaultPort}
                value={draft.service_port}
                onChange={(event) => update('service_port', event.target.value)}
              />
            </Field>
            <Field label="Scheme" htmlFor="service_scheme">
              <Select
                id="service_scheme"
                value={draft.service_scheme}
                onChange={(event) => update('service_scheme', event.target.value as 'http' | 'https')}
              >
                <option value="http">http</option>
                <option value="https">https</option>
              </Select>
            </Field>
          </div>
        </>
      ) : (
        <>
          <Field
            label="Address"
            htmlFor="url"
            hint="The base address, without the provider's own API path."
          >
            <TextInput
              id="url"
              type="url"
              required
              className="font-mono text-[12.5px]"
              placeholder="https://vmselect.example.com:8481"
              value={draft.url}
              onChange={(event) => update('url', event.target.value)}
            />
          </Field>

          <label className="flex items-start gap-2.5 text-[12.5px] leading-relaxed text-muted">
            <input
              type="checkbox"
              className="mt-0.5 size-3.5 accent-[var(--color-accent)]"
              checked={draft.insecure_skip_verify}
              onChange={(event) => update('insecure_skip_verify', event.target.checked)}
            />
            <span>
              Skip TLS verification. An internal certificate is a reason to trust its CA, not a
              reason to stop checking — leave this off unless nothing else works.
            </span>
          </label>
        </>
      )}

      <Field
        label="Path prefix"
        htmlFor="path_prefix"
        hint="What sits in front of the provider's own API paths. vmselect serves it under /select/0/prometheus; Mimir under /prometheus; most others at the root."
      >
        <TextInput
          id="path_prefix"
          className="font-mono text-[12.5px]"
          placeholder={info.defaultPrefix || '/'}
          value={draft.path_prefix}
          onChange={(event) => update('path_prefix', event.target.value)}
        />
      </Field>

      {/* The one thing an Explore deep link cannot be built without. It sits on
          the datasource rather than on the Grafana row because it identifies
          *this* backend: one Grafana holds the metrics datasource and the logs
          one, and they are two different uids. */}
      <Field
        label="Grafana datasource uid"
        htmlFor="grafana_datasource"
        hint="Optional. From this backend's datasource page in the cluster's Grafana (Settings → Data sources → the uid in its URL). With it, a chart here opens the same query in Grafana."
      >
        <TextInput
          id="grafana_datasource"
          className="font-mono text-[12.5px]"
          placeholder="P1809F7CD0C75ACF3"
          value={draft.grafana_datasource}
          onChange={(event) => update('grafana_datasource', event.target.value)}
        />
      </Field>

      <Field label="Authentication" htmlFor="auth_mode">
        <Select
          id="auth_mode"
          value={draft.auth_mode}
          onChange={(event) => update('auth_mode', event.target.value as DatasourceAuth)}
        >
          <option value="none">None</option>
          <option value="bearer">Bearer token</option>
          <option value="basic">Username and password</option>
        </Select>
      </Field>

      {draft.auth_mode === 'basic' ? (
        <Field label="Username" htmlFor="username">
          <TextInput
            id="username"
            required
            value={draft.username}
            onChange={(event) => update('username', event.target.value)}
          />
        </Field>
      ) : null}

      {draft.auth_mode !== 'none' ? (
        <Field
          label={draft.auth_mode === 'bearer' ? 'Token' : 'Password'}
          htmlFor="credential"
          hint={
            hasStoredCredential
              ? 'A credential is stored. Leave this empty to keep it.'
              : 'Stored on the server and never sent back to a browser.'
          }
        >
          <TextInput
            id="credential"
            type="password"
            autoComplete="new-password"
            placeholder={hasStoredCredential ? '••••••••' : ''}
            value={draft.credential}
            onChange={(event) => update('credential', event.target.value)}
          />
        </Field>
      ) : null}

      {inCluster && draft.auth_mode !== 'none' ? (
        <Notice tone="info">
          An in-cluster read is made by the cluster&rsquo;s own API server on KubeMG&rsquo;s behalf,
          so there is nowhere to put this header. A datasource that needs a credential should be
          given as an external address.
        </Notice>
      ) : null}

      <label className="flex items-center gap-2.5 text-[12.5px] text-muted">
        <input
          type="checkbox"
          className="size-3.5 accent-[var(--color-accent)]"
          checked={draft.enabled}
          onChange={(event) => update('enabled', event.target.checked)}
        />
        Use this source
      </label>
    </Sheet>
  )
}
