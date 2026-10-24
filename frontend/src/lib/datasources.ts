import type {
  DatasourceKind,
  DatasourceProvider,
  ObservabilitySource,
} from '../api/types'
import type { Tone } from './status'

/**
 * What KubeMG knows about each series backend, in one place: the sidebar of
 * choices, the port a fresh form starts on, and the path prefix that is the
 * single most common reason a correct address answers 404.
 *
 * The backend validates all of this again — this table exists so the form can
 * fill itself in, not so the browser can decide what is allowed.
 */
export interface ProviderInfo {
  label: string
  kind: DatasourceKind
  /** The port the provider's own charts conventionally serve its query API on. */
  defaultPort: string
  /** Where that API lives when it is not at the root. */
  defaultPrefix: string
  /** What an operator should recognise, in one line. */
  hint: string
}

export const PROVIDERS: Record<DatasourceProvider, ProviderInfo> = {
  victoriametrics: {
    label: 'VictoriaMetrics',
    kind: 'metrics',
    defaultPort: '8428',
    defaultPrefix: '',
    hint: 'Single-node serves the Prometheus API at the root on 8428. A cluster install answers on vmselect:8481 under /select/0/prometheus.',
  },
  prometheus: {
    label: 'Prometheus',
    kind: 'metrics',
    defaultPort: '9090',
    defaultPrefix: '',
    hint: 'The Service kube-prometheus-stack installs, usually in the monitoring namespace.',
  },
  thanos: {
    label: 'Thanos',
    kind: 'metrics',
    defaultPort: '9090',
    defaultPrefix: '',
    hint: 'Point at the Querier, not at a sidecar or the store gateway.',
  },
  mimir: {
    label: 'Mimir',
    kind: 'metrics',
    defaultPort: '8080',
    defaultPrefix: '/prometheus',
    hint: 'Point at the query frontend or the gateway. A multi-tenant Mimir also needs its tenant header, which kubemg does not send yet.',
  },
  victorialogs: {
    label: 'VictoriaLogs',
    kind: 'logs',
    defaultPort: '9428',
    defaultPrefix: '',
    hint: 'Single-node serves LogsQL on 9428.',
  },
  loki: {
    label: 'Loki',
    kind: 'logs',
    defaultPort: '3100',
    defaultPrefix: '',
    hint: 'Point at the gateway or the query frontend, not at an ingester.',
  },
  alertmanager: {
    label: 'Alertmanager',
    kind: 'alerts',
    defaultPort: '9093',
    defaultPrefix: '',
    hint: 'The Service kube-prometheus-stack installs next to Prometheus — not the headless alertmanager-operated one.',
  },
  argocd: {
    label: 'Argo CD',
    kind: 'gitops',
    defaultPort: '443',
    defaultPrefix: '',
    hint: 'The argocd-server Service. Read with an account token — an account with the apiKey capability, bound to role:readonly.',
  },
}

export const METRICS_PROVIDERS: DatasourceProvider[] = [
  'victoriametrics',
  'prometheus',
  'thanos',
  'mimir',
]

export const LOGS_PROVIDERS: DatasourceProvider[] = ['victorialogs', 'loki']

export const ALERTS_PROVIDERS: DatasourceProvider[] = ['alertmanager']

export const GITOPS_PROVIDERS: DatasourceProvider[] = ['argocd']

export function providersFor(kind: DatasourceKind): DatasourceProvider[] {
  if (kind === 'alerts') return ALERTS_PROVIDERS
  if (kind === 'gitops') return GITOPS_PROVIDERS
  return kind === 'metrics' ? METRICS_PROVIDERS : LOGS_PROVIDERS
}

export const KIND_LABEL: Record<DatasourceKind, string> = {
  metrics: 'Metrics',
  logs: 'Logs',
  alerts: 'Alerts',
  gitops: 'GitOps',
}

/** What each kind is *for*, said once rather than in every panel. */
export const KIND_PURPOSE: Record<DatasourceKind, string> = {
  metrics:
    'History behind the live meters. Without it kubemg can only show the last couple of minutes the cluster keeps itself.',
  logs: 'Searchable logs across pods. Without it a log is only readable while the pod that wrote it is still alive.',
  alerts:
    'What is firing and what is silenced, on every object. It is also what alarms need: an alarm is a PrometheusRule carrying the labels the cluster\'s Prometheus loads rules by.',
  gitops:
    'What an object Argo CD tracks should look like, field by field, next to what it is. Sync state shows without it; the comparison needs it.',
}

export const DATASOURCE_KINDS: DatasourceKind[] = ['metrics', 'logs', 'alerts', 'gitops']

/** Whether a kind is read with a credential and nothing else: Argo CD answers
    nothing useful anonymously and takes no basic login as an API credential. */
export function bearerOnly(kind: DatasourceKind): boolean {
  return kind === 'gitops'
}

/** Whether a kind carries a Grafana datasource uid. Only series backends do. */
export function hasGrafanaRef(kind: DatasourceKind): boolean {
  return kind === 'metrics' || kind === 'logs'
}

/** `k=v, k=v` as an operator types it, read into a label map. */
export function parseRuleLabels(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const pair of text.split(',')) {
    const [key, ...rest] = pair.split('=')
    if (key.trim()) out[key.trim()] = rest.join('=').trim()
  }
  return out
}

export function formatRuleLabels(labels: Record<string, string> | undefined): string {
  return Object.entries(labels ?? {})
    .map(([key, value]) => `${key}=${value}`)
    .join(', ')
}

/** How a source's last check reads on the deck. */
export function sourceTone(source: ObservabilitySource): Tone {
  if (!source.enabled) return 'idle'
  if (source.last_status === 'healthy') return 'ok'
  if (source.last_status === 'unhealthy') return 'bad'
  return 'idle'
}

export function sourceStateLabel(source: ObservabilitySource): string {
  if (!source.enabled) return 'Paused'
  if (source.last_status === 'healthy') return 'Answering'
  if (source.last_status === 'unhealthy') return 'Not answering'
  return 'Never checked'
}
